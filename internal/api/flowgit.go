package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/flows"

	"github.com/google/uuid"
)

// The git nodes. Where it runs: on the runner that holds the repository; the hub asks over
// RPC (/api/review/status, /api/review/merge-options) and keeps only the state. A row without
// a runner is answered in-process, as a legacy case, exactly like the Review button.

// flowReview finds the branch of a card: its Review row, or else what its session worked in.
func (h *Handler) flowReview(card dbpkg.KanbanCard) (dbpkg.CardReview, bool) {
	if rv, err := dbpkg.GetCardReview(h.db, card.ID); err == nil {
		return rv, true
	}
	sess, err := dbpkg.GetSessionByCard(h.db, card.ID)
	if err != nil {
		return dbpkg.CardReview{}, false
	}
	return h.resolveReview(sess.ID, card.ID)
}

func (f flowHost) GitStatus(cardID string) (flows.GitState, error) {
	card, err := dbpkg.GetKanbanCard(f.h.db, cardID)
	if err != nil {
		return flows.GitState{}, err
	}
	rv, ok := f.h.flowReview(card)
	if !ok {
		return flows.GitState{State: flows.PortNothing, Files: []string{}, ConflictFiles: []string{}}, nil
	}
	st := f.h.reviewStatusOf(rv)
	if st.Error != "" {
		return flows.GitState{}, errors.New(st.Error)
	}
	state := flows.PortClean
	switch {
	case st.Merged:
		state = flows.PortNothing
	case st.Conflicts:
		state = flows.PortConflicts
	case st.Dirty:
		state = flows.PortDirty
	}
	return flows.GitState{
		State: state, Branch: st.Branch, Base: st.Base, Ahead: st.Ahead, Files: st.Files,
		ConflictFiles: st.ConflictFiles, WorktreePath: st.WorktreePath,
	}, nil
}

// finishMerge is the hub's half of a merge: the state, once the runner did the git work.
func (h *Handler) finishMerge(card dbpkg.KanbanCard, rv dbpkg.CardReview, res mergeResult, moveToDone bool, cause dbpkg.EventCause) (dbpkg.KanbanCard, error) {
	if res.WorktreeRemoved {
		// the agent's directory is gone: its chat can no longer start claude there
		_ = dbpkg.MarkCardSessionsWorktreeRemoved(h.db, card.ID)
	}
	_ = dbpkg.DeleteCardReview(h.db, card.ID)
	into := res.Base
	if into == "" {
		into = "its base branch"
	}
	_ = dbpkg.InsertCardComment(h.db, dbpkg.CardComment{
		ID: uuid.New().String(), CardID: card.ID, Author: "lucode", Kind: "comment",
		Body: fmt.Sprintf("Merged %s into %s.", rv.Branch, into),
	})
	if !moveToDone {
		return card, nil
	}
	moved, err := h.moveCardToKindCause(card, dbpkg.KanbanDone, cause)
	if err != nil {
		return card, err
	}
	h.releaseDependents(moved)
	return moved, nil
}

func (h *Handler) mergeWithOptionsOnRunner(ctx context.Context, rv dbpkg.CardReview, title string, opts mergeOptions) (mergeResult, int, error) {
	if !h.isRemoteRunner(rv.RunnerID) {
		return mergeWithOptions(rv, title, opts)
	}
	if opts == (mergeOptions{}) {
		return h.mergeOnRunner(rv, title) // what any runner understands, however old
	}
	var res mergeResult
	code, err := h.runnerCall(ctx, rv.RunnerID, http.MethodPost, "/api/review/merge-options", mergeOptionsRequest{refOf(rv), title, opts}, &res)
	if err != nil {
		// A runner too old for the options must not merge and ignore them: that would delete
		// the worktree someone asked to keep.
		if code == http.StatusNotFound {
			return res, code, fmt.Errorf("runner %s is too old for these merge options: update it, or use the default options. Nothing was merged", rv.RunnerID)
		}
		return res, code, err
	}
	return res, http.StatusOK, nil
}

// GitMerge merges the card's branch. The port says how it went; an error means it failed.
func (f flowHost) GitMerge(ctx context.Context, cardID string, o flows.MergeOptions, cause dbpkg.EventCause) (string, map[string]any, error) {
	h := f.h
	card, err := dbpkg.GetKanbanCard(h.db, cardID)
	if err != nil {
		return "", nil, err
	}
	rv, ok := h.flowReview(card)
	if !ok {
		return "", nil, errors.New("this card has no branch to merge")
	}
	st := h.reviewStatusOf(rv)
	if st.Error != "" {
		return "", nil, errors.New(st.Error)
	}
	if st.Conflicts || st.Dirty {
		why := "the worktree has uncommitted changes"
		if st.Conflicts {
			why = "the branch conflicts with " + st.Base
		}
		return flows.PortConflicts, map[string]any{"reason": why, "conflict_files": st.ConflictFiles}, nil
	}
	opts := mergeOptions{KeepWorktree: !o.DeleteWorktree, KeepBranch: !o.DeleteBranch, Strategy: o.Strategy, Message: o.Message}
	if opts.Strategy == "merge" {
		opts.Strategy = ""
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	res, code, err := h.mergeWithOptionsOnRunner(ctx, rv, card.Title, opts)
	if err != nil {
		if code == http.StatusConflict {
			return flows.PortConflicts, map[string]any{"reason": err.Error()}, nil // git refused and changed nothing
		}
		return "", nil, err
	}
	moved, err := h.finishMerge(card, rv, res, o.MoveToDone, cause)
	if err != nil {
		return "", nil, fmt.Errorf("merged, but could not move the task to Done: %w", err)
	}
	h.notifyTaskUpdated("", moved)
	return flows.PortMerged, map[string]any{
		"base": res.Base, "branch": rv.Branch, "worktree_removed": res.WorktreeRemoved, "warnings": res.Warnings,
	}, nil
}

// GitMergeFinished completes the hub's half of a merge the hub stopped in the middle of, once
// the branch is known to be merged.
func (f flowHost) GitMergeFinished(cardID string, o flows.MergeOptions, cause dbpkg.EventCause) error {
	h := f.h
	card, err := dbpkg.GetKanbanCard(h.db, cardID)
	if err != nil {
		return err
	}
	rv, ok := h.flowReview(card)
	if !ok {
		rv = dbpkg.CardReview{CardID: cardID, Branch: "the task's branch"}
	}
	moved, err := h.finishMerge(card, rv, mergeResult{}, o.MoveToDone, cause)
	if err != nil {
		return err
	}
	h.notifyTaskUpdated("", moved)
	return nil
}

// NotifySupervisor tells the supervisor of the card's agent. A card without one has nobody to tell.
func (f flowHost) NotifySupervisor(cardID, text string) error {
	card, err := dbpkg.GetKanbanCard(f.h.db, cardID)
	if err != nil {
		return err
	}
	agent, err := dbpkg.GetAgent(f.h.db, card.AgentID)
	if err != nil {
		return nil
	}
	f.h.notifySupervisor(agent, card, text)
	return nil
}

// reviewRowFor keeps the branch of a card entering Review, so the Review dialog can find it.
func (h *Handler) reviewRowFor(card dbpkg.KanbanCard) {
	if _, err := dbpkg.GetCardReview(h.db, card.ID); err == nil {
		return
	}
	sess, err := dbpkg.GetSessionByCard(h.db, card.ID)
	if err != nil {
		return
	}
	if rv, ok := h.resolveReview(sess.ID, card.ID); ok {
		_ = dbpkg.SaveCardReview(h.db, rv)
	}
}
