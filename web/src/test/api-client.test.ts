import { describe, expect, it } from "vitest";
import { ApiClient, ApiRequestError, decodeError, withCursor } from "../generated/api-client";

function stubClient(status: number, body: unknown): { client: ApiClient; calls: Array<{ url: string; init: RequestInit }> } {
  const calls: Array<{ url: string; init: RequestInit }> = [];
  const client = new ApiClient("http://vertex.local", (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init: init ?? {} });
    return new Response(typeof body === "string" ? body : JSON.stringify(body), { status });
  }) as typeof fetch);
  return { client, calls };
}

describe("generated api client", () => {
  it("binds browser fetch to the global object", async () => {
    const client = new ApiClient('http://vertex.local', async function (this: unknown) {
      expect(this).toBe(globalThis);
      return new Response('{}', { status: 200 });
    } as typeof fetch);
    await client.getAuthStatus();
  });
  it("listSites 调用合同路径且不手写字符串", async () => {
    const { client, calls } = stubClient(200, { items: [] });
    const result = await client.listSites();
    expect(result).toEqual({ items: [] });
    expect(calls[0].url).toBe("http://vertex.local/api/v1/sites");
    expect(calls[0].init.method).toBe("GET");
  });

  it("路径参数被编码进 URL", async () => {
    const { client, calls } = stubClient(200, { id: "job-1" });
    await client.getJob("job 1/2");
    expect(calls[0].url).toBe("http://vertex.local/api/v1/jobs/job%201%2F2");
  });

  it("updateRule 透传 If-Match 请求头", async () => {
    const { client, calls } = stubClient(200, { id: "rule-1" });
    await client.updateRule("rule-1", { body: { enabled: false }, headers: { "If-Match": '"2"' } });
    expect(calls[0].init.headers).toMatchObject({ "Content-Type": "application/json", "If-Match": '"2"' });
  });
  it("updateFeed sends If-Match and JSON body", async () => {
    const { client, calls } = stubClient(200, { id: "feed-1" });
    await client.updateFeed("feed-1", { body: { enabled: false }, headers: { "If-Match": '"2026-08-29T08:00:00Z"' } });
    expect(calls[0].url).toBe("http://vertex.local/api/v1/feeds/feed-1");
    expect(calls[0].init.method).toBe("PUT");
    expect(calls[0].init.headers).toMatchObject({ "Content-Type": "application/json", "If-Match": '"2026-08-29T08:00:00Z"' });
  });
  it("错误 envelope 按合同错误码分支解码", async () => {
    const { client } = stubClient(409, {
      error: { code: "REVISION_CONFLICT", message: "revision moved", request_id: "req-1", retryable: true },
    });
    await expect(client.listJobs()).rejects.toMatchObject({
      name: "ApiRequestError",
      status: 409,
      body: { code: "REVISION_CONFLICT", retryable: true, request_id: "req-1" },
    });
  });


  it("未知错误码归一化为 INTERNAL_ERROR", () => {
    const decoded = decodeError(500, { error: { code: "NOT_IN_CONTRACT", message: "x", request_id: "r", retryable: true } });
    expect(decoded.body.code).toBe("INTERNAL_ERROR");
    expect(decoded.body.retryable).toBe(false);
  });

  it("cursor 辅助透传 opaque cursor", () => {
    const query = withCursor({}, "opaque-cursor", 50);
    expect(query).toEqual({ cursor: "opaque-cursor", limit: "50" });
    const withoutLimit = withCursor({ instance_id: "qb-1" });
    expect(withoutLimit).toEqual({ instance_id: "qb-1" });
  });
});
