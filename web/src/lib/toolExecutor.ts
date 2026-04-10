/**
 * Browser-side tool execution engine.
 * Runs user-defined JavaScript in a sandboxed Function() context.
 * For server-side ("orchestrator") execution, the API endpoint is used instead.
 */

import type { UserTool, ToolExecuteResult } from '../types'
import * as api from '../api'

/**
 * Validate arguments against a JSON Schema (basic validation).
 * Checks required fields and type matches for top-level properties.
 */
export function validateArgs(args: Record<string, unknown>, schemaStr: string): string | null {
  let schema: {
    type?: string
    properties?: Record<string, { type?: string }>
    required?: string[]
  }

  try {
    schema = JSON.parse(schemaStr)
  } catch {
    return 'Invalid JSON schema'
  }

  if (schema.required) {
    for (const key of schema.required) {
      if (args[key] === undefined || args[key] === null) {
        return `Missing required argument: ${key}`
      }
    }
  }

  if (schema.properties) {
    for (const [key, prop] of Object.entries(schema.properties)) {
      const value = args[key]
      if (value === undefined) continue

      if (prop.type) {
        const actualType = Array.isArray(value) ? 'array' : typeof value
        if (prop.type === 'integer') {
          if (typeof value !== 'number' || !Number.isInteger(value)) {
            return `Argument "${key}" must be an integer`
          }
        } else if (actualType !== prop.type) {
          return `Argument "${key}" must be of type ${prop.type}, got ${actualType}`
        }
      }
    }
  }

  return null
}

/**
 * Execute a tool in the appropriate environment.
 * - "browser": runs JS in a sandboxed Function() context
 * - "orchestrator": calls the server-side API endpoint
 */
export async function executeTool(
  tool: UserTool,
  args: Record<string, unknown>,
): Promise<ToolExecuteResult> {
  // Validate arguments
  const validationError = validateArgs(args, tool.input_schema)
  if (validationError) {
    return { success: false, error: validationError }
  }

  if (tool.execution_env === 'orchestrator') {
    return executeServerSide(tool.id, args)
  }

  return executeBrowserSide(tool.code, args)
}

/**
 * Execute tool code in the browser using Function() constructor.
 * The code receives `args` as a parameter containing the parsed arguments.
 */
async function executeBrowserSide(
  code: string,
  args: Record<string, unknown>,
): Promise<ToolExecuteResult> {
  try {
    // Create a sandboxed function with `args` as parameter
    // The code can return a value or use async/await
    const fn = new Function('args', `
      "use strict";
      return (async function() {
        ${code}
      })();
    `)

    const result = await fn(args)
    return { success: true, result }
  } catch (err: unknown) {
    const message = err instanceof Error ? err.message : String(err)
    return { success: false, error: message }
  }
}

/**
 * Execute tool on the server via API.
 */
async function executeServerSide(
  toolId: string,
  args: Record<string, unknown>,
): Promise<ToolExecuteResult> {
  try {
    return await api.executeUserTool(toolId, args)
  } catch (err: unknown) {
    const message = err instanceof Error ? err.message : String(err)
    return { success: false, error: message }
  }
}
