import { http } from "@/lib/api";

export interface SideModel {
  model: string;
  name: string;
  format: string;
  profile_id: number;
}
export interface SideExchange {
  id: string;
  ordinal: number;
  client_request_id: string;
  question: string;
  answer: string;
  status: "running" | "completed" | "failed" | "cancelled" | "interrupted";
  error?: string;
  model: SideModel;
  snapshot_at: string;
  created_at: string;
  sequence: number;
  context?: {
    phase?: "preparing" | "summarizing_history" | "compressing_snapshot" | "retrying" | "answering";
    recent_exchanges: number;
    history_summarized: boolean;
    snapshot_summarized: boolean;
    estimated_input_tokens?: number;
    input_budget?: number;
    output_tokens?: number;
    overflow_retried?: boolean;
  };
}
export interface SideHistory {
  items: SideExchange[];
  current: SideExchange | null;
  next_cursor: number;
  snapshot: { captured_at: string; model: SideModel; available: boolean; reason: string } | null;
}

async function request<T>(path: string, method = "GET", body?: unknown): Promise<T> {
  return http<T>(path.replace(/^\/api/, ""), { method, body: body === undefined ? undefined : JSON.stringify(body) });
}

export const sideAPI = {
  // History must be normalized before it is handed to the caller: once items is not an array, the consumer's setState updater throws,
  // and React defers the updater's exception to rethrow it during render — by then the caller's catch can no longer reach it,
  // and the error boundary takes over the whole page.
  history: async (parent: string, before = 0) => {
    const data = await request<Partial<SideHistory>>(`${parent}/side-questions?before=${before}`);
    return {
      items: Array.isArray(data?.items) ? data.items : [],
      current: data?.current ?? null,
      next_cursor: Number(data?.next_cursor) || 0,
      snapshot: data?.snapshot ?? null,
    } satisfies SideHistory;
  },
  ask: (parent: string, question: string, client_request_id: string) =>
    request<SideExchange>(`${parent}/side-questions`, "POST", { question, client_request_id }),
  clear: (parent: string) => request(`${parent}/side-questions`, "DELETE"),
  cancel: (id: string) => request(`/api/side-questions/${id}/cancel`, "POST"),
};

export function isBtwCommand(text: string) {
  return /^\/btw(?:\s|$)/.test(text.trim());
}
