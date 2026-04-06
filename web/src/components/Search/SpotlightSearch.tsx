import React, { useState, useEffect, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { motion } from 'framer-motion'
import * as api from '../../api'
import { useStore } from '../../store'
import type { SearchResult } from '../../types'
import { IconFolder, IconMessage } from '@tabler/icons-react'

interface Props {
  isOpen: boolean
  onClose: () => void
}

export const SpotlightSearch: React.FC<Props> = ({ isOpen, onClose }) => {
  const navigate = useNavigate()
  const inputRef = useRef<HTMLInputElement>(null)
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<SearchResult[]>([])
  const [selectedIndex, setSelectedIndex] = useState(0)
  const [isLoading, setIsLoading] = useState(false)
  const setJumpToMessage = useStore(s => s.setJumpToMessage)

  // Auto-focus input when modal opens
  useEffect(() => {
    if (isOpen) {
      setQuery('')
      setResults([])
      setSelectedIndex(0)
      inputRef.current?.focus()
    }
  }, [isOpen])

  // Debounced search
  useEffect(() => {
    if (!query || query.length < 3) {
      setResults([])
      return
    }

    setIsLoading(true)
    const timer = setTimeout(async () => {
      try {
        const { results: searchResults } = await api.search(query)
        setResults(searchResults)
        setSelectedIndex(0)
      } catch (err) {
        console.error('Search error:', err)
        setResults([])
      } finally {
        setIsLoading(false)
      }
    }, 300)

    return () => clearTimeout(timer)
  }, [query])

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Escape') {
      onClose()
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      setSelectedIndex(i => Math.min(i + 1, results.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setSelectedIndex(i => Math.max(i - 1, 0))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      if (results.length > 0) {
        activateResult(results[selectedIndex])
      }
    }
  }

  const activateResult = (result: SearchResult) => {
    if (result.type === 'project') {
      navigate(`/projects/${result.project_id}`)
    } else if (result.type === 'session') {
      navigate(`/projects/${result.project_id}/sessions/${result.session_id}`)
    } else if (result.type === 'message') {
      setJumpToMessage(result.id)
      navigate(`/projects/${result.project_id}/sessions/${result.session_id}`)
    }
    onClose()
  }

  if (!isOpen) return null

  // Group results by type
  const projects = results.filter(r => r.type === 'project')
  const sessions = results.filter(r => r.type === 'session')
  const messages = results.filter(r => r.type === 'message')

  let flatResults: SearchResult[] = []
  if (projects.length > 0) flatResults = flatResults.concat(projects)
  if (sessions.length > 0) flatResults = flatResults.concat(sessions)
  if (messages.length > 0) flatResults = flatResults.concat(messages)

  return (
    <>
      <motion.div
        className="spotlight-backdrop"
        initial={{ opacity: 0 }}
        animate={{ opacity: 1 }}
        exit={{ opacity: 0 }}
        onClick={onClose}
      />
      <div className="spotlight-modal-container" onClick={onClose}>
        <motion.div
          className="spotlight-modal"
          initial={{ opacity: 0, scale: 0.95 }}
          animate={{ opacity: 1, scale: 1 }}
          exit={{ opacity: 0, scale: 0.95 }}
          transition={{ duration: 0.15 }}
          onClick={e => e.stopPropagation()}
        >
          <input
            ref={inputRef}
            type="text"
            placeholder="Search projects, sessions, messages (3+ chars)…"
            value={query}
            onChange={e => setQuery(e.target.value)}
            onKeyDown={handleKeyDown}
            className="spotlight-input"
            autoComplete="off"
          />

          <div className="spotlight-results">
            {isLoading && <div className="spotlight-loading">Searching…</div>}

            {!isLoading && flatResults.length === 0 && query.length >= 3 && (
              <div className="spotlight-empty">No results found</div>
            )}

            {!isLoading && flatResults.length > 0 && (
              <>
                {projects.length > 0 && (
                  <>
                    <div className="spotlight-group-header">Projects</div>
                    {projects.map((result) => (
                      <ResultItem
                        key={result.id}
                        result={result}
                        isSelected={selectedIndex === flatResults.indexOf(result)}
                        onClick={() => activateResult(result)}
                      />
                    ))}
                  </>
                )}

                {sessions.length > 0 && (
                  <>
                    <div className="spotlight-group-header">Sessions</div>
                    {sessions.map((result) => (
                      <ResultItem
                        key={result.id}
                        result={result}
                        isSelected={selectedIndex === flatResults.indexOf(result)}
                        onClick={() => activateResult(result)}
                      />
                    ))}
                  </>
                )}

                {messages.length > 0 && (
                  <>
                    <div className="spotlight-group-header">Messages</div>
                    {messages.map((result) => (
                      <ResultItem
                        key={result.id}
                        result={result}
                        isSelected={selectedIndex === flatResults.indexOf(result)}
                        onClick={() => activateResult(result)}
                      />
                    ))}
                  </>
                )}
              </>
            )}
          </div>
        </motion.div>
      </div>
    </>
  )
}

interface ResultItemProps {
  result: SearchResult
  isSelected: boolean
  onClick: () => void
}

const ResultItem: React.FC<ResultItemProps> = ({ result, isSelected, onClick }) => {
  const getIcon = () => {
    switch (result.type) {
      case 'project':
        return <IconFolder/>
      case 'session':
        return <IconMessage/>
      case 'message':
        return <IconMessage/>
    }
  }

  return (
    <div
      className={`spotlight-result ${isSelected ? 'spotlight-result--selected' : ''}`}
      onClick={onClick}
    >
      <span className="spotlight-result__icon">{getIcon()}</span>
      <div className="spotlight-result__content">
        <div className="spotlight-result__title">{result.title}</div>
        {result.snippet && <div className="spotlight-result__snippet">{result.snippet}</div>}
      </div>
    </div>
  )
}
