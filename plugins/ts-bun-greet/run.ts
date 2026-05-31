#!/usr/bin/env bun
// ts-bun-greet: TypeScript/Bun example plugin for Ductile Gateway
// Demonstrates protocol v2 JSON I/O over stdin/stdout.

// --- Protocol v2 types ---

interface Request {
  protocol: number;
  job_id: string;
  command: "poll" | "handle" | "health" | "init";
  config: Record<string, unknown>;
  state: Record<string, unknown>;
  event?: Record<string, unknown>;
  deadline_at?: string;
}

interface LogEntry {
  level: "debug" | "info" | "warn" | "error";
  message: string;
}

interface Response {
  status: "ok" | "error";
  result?: string;
  error?: string;
  retry?: boolean;
  events?: Array<{ type: string; payload: Record<string, unknown>; dedupe_key?: string }>;
  state_updates?: Record<string, unknown>;
  logs?: LogEntry[];
}

// --- Read request from stdin ---

const input = await Bun.stdin.text();
const request: Request = JSON.parse(input);

// --- Handle commands ---

function snapshotState(message: string, now: string): Record<string, unknown> {
  return {
    last_run: now,
    last_greeting: message,
  };
}

function poll(req: Request): Response {
  const greeting = (req.config.greeting as string) || "Hello";
  const name = (req.config.name as string) || "World";
  const now = new Date().toISOString();

  const message = `${greeting}, ${name}!`;
  return {
    status: "ok",
    result: message,
    events: [],
    state_updates: snapshotState(message, now),
    logs: [
      { level: "info", message: `${message} (job: ${req.job_id})` },
    ],
  };
}

function health(req: Request): Response {
  return {
    status: "ok",
    result: "healthy",
    logs: [{ level: "info", message: "healthy" }],
  };
}

let response: Response;

switch (request.command) {
  case "poll":
    response = poll(request);
    break;
  case "health":
    response = health(request);
    break;
  default:
    response = {
      status: "error",
      error: `unknown command: ${request.command}`,
      retry: false,
      logs: [{ level: "error", message: `unknown command: ${request.command}` }],
    };
}

// --- Write response to stdout ---

process.stdout.write(JSON.stringify(response));
