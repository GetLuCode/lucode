/**
 * useCommandDetection — processes voice transcripts through hotword detection
 * and LLM command classification pipeline.
 *
 * Flow:
 * 1. Transcript arrives from STT
 * 2. Check for hotword prefix
 * 3. If hotword found → send to LLM command detection API
 * 4. If tool_call returned → execute tool (browser or server)
 * 5. If no match → pass transcript to input
 */

import { useCallback, useRef } from 'react'
import { detectCommand } from '../api'
import { executeTool } from '../lib/toolExecutor'
import type { UserTool } from '../types'

export interface CommandDetectionOptions {
  hotword: string
  accountId: string | null
  tools: UserTool[]
  onPassthrough: (text: string) => void
  onCommandStart: () => void
  onCommandResult: (result: CommandResult) => void
  onCommandError: (error: string) => void
}

export interface CommandResult {
  toolName: string
  arguments: Record<string, unknown>
  executionResult: unknown
  success: boolean
  error?: string
}

export function useCommandDetection({
  hotword,
  accountId,
  tools,
  onPassthrough,
  onCommandStart,
  onCommandResult,
  onCommandError,
}: CommandDetectionOptions) {
  const processingRef = useRef(false)

  const processTranscript = useCallback(async (transcript: string) => {
    if (processingRef.current) {
      onPassthrough(transcript)
      return
    }

    // No hotword configured or no tools → passthrough
    if (!hotword || tools.length === 0 || !accountId) {
      onPassthrough(transcript)
      return
    }

    // Check for hotword (case-insensitive)
    const normalized = transcript.trim().toLowerCase()
    const hotwordLower = hotword.trim().toLowerCase()

    if (!normalized.startsWith(hotwordLower)) {
      // No hotword detected → passthrough to input
      onPassthrough(transcript)
      return
    }

    // Extract command text after hotword
    const commandText = transcript.trim().slice(hotword.trim().length).trim()
    if (!commandText) {
      onPassthrough(transcript)
      return
    }

    // Hotword detected — start command processing
    processingRef.current = true
    onCommandStart()

    try {
      // Send to LLM for classification
      const result = await detectCommand(commandText, accountId)

      if (!result.tool_call) {
        // No tool matched — pass through the original text
        onPassthrough(transcript)
        processingRef.current = false
        return
      }

      // Find the tool definition
      const tool = tools.find(t => t.name === result.tool_call!.tool_name)
      if (!tool) {
        onCommandError(`Tool "${result.tool_call.tool_name}" not found`)
        processingRef.current = false
        return
      }

      // Execute the tool
      const execResult = await executeTool(tool, result.tool_call.arguments)

      onCommandResult({
        toolName: result.tool_call.tool_name,
        arguments: result.tool_call.arguments,
        executionResult: execResult.result,
        success: execResult.success,
        error: execResult.error,
      })
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err)
      onCommandError(message)
    } finally {
      processingRef.current = false
    }
  }, [hotword, accountId, tools, onPassthrough, onCommandStart, onCommandResult, onCommandError])

  return { processTranscript, isProcessing: processingRef }
}
