package natsbus

import "github.com/nats-io/nats.go"

// Subjects of the Commands tab. The terminal's bytes travel on plain NATS subjects, one per process
// (the id is the command's id), so neither side needs a port of its own:
//
//	lucode.term.<id>.out   runner → hub   8-byte big-endian run + 8-byte offset + output bytes
//	lucode.term.<id>.in    hub → runner   keystrokes
//	lucode.term.<id>.ctl   hub → runner   JSON, {"type":"resize","cols":..,"rows":..}
//	lucode.term.status     runner → hub   JSON termrun.Status of any process that changed
func TermOutSubject(id string) string { return "lucode.term." + id + ".out" }
func TermInSubject(id string) string  { return "lucode.term." + id + ".in" }
func TermCtlSubject(id string) string { return "lucode.term." + id + ".ctl" }

const TermStatusSubject = "lucode.term.status"

// PublishRaw sends bytes on a subject as they are.
func (b *Bus) PublishRaw(subject string, data []byte) error { return b.nc.Publish(subject, data) }

// SubscribeRaw calls fn for each message of a subject (wildcards allowed), in order. The returned
// function ends the subscription.
func (b *Bus) SubscribeRaw(subject string, fn func(subject string, data []byte)) (func(), error) {
	sub, err := b.nc.Subscribe(subject, func(m *nats.Msg) { fn(m.Subject, m.Data) })
	if err != nil {
		return nil, err
	}
	return func() { _ = sub.Unsubscribe() }, nil
}
