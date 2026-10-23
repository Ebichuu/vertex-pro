package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type sourceData struct {
	openAPI    map[string]any
	errors     []string
	retryable  map[string]string
	events     []string
	userStatus map[string]string
	states     map[string][]string
}

var (
	yamlDomainPattern = regexp.MustCompile(`^  ([a-z][a-z0-9_]*)\s*:$`)
	yamlValuesPattern = regexp.MustCompile(`^    values: \[(.*)\]$`)
	codePattern       = regexp.MustCompile(`^  ([A-Z][A-Z0-9_]*)\s*:\s*\{`)
	retryablePattern  = regexp.MustCompile(`^  ([A-Z][A-Z0-9_]*)\s*:\s*\{ http_status: \d+, retryable: (true|false) \}$`)
	eventPattern      = regexp.MustCompile(`^  ([a-z][a-z0-9_.]*):$`)
	statusPattern     = regexp.MustCompile(`^  ([A-Z][A-Z0-9_]*)\s*:\s*(.+)$`)
)

func main() {
	check := flag.Bool("check", false, "check generated files without writing")
	flag.Parse()

	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	data, sources, err := loadSources(root)
	if err != nil {
		fatal(err)
	}
	goOutput := renderGo(data, digest(sources))
	tsOutput := renderTypeScript(data, digest(sources))
	modelsOutput, err := renderModels(data.openAPI)
	if err != nil {
		fatal(err)
	}
	clientOutput, err := renderClient(data.openAPI)
	if err != nil {
		fatal(err)
	}
	clientPath := filepath.Join(root, "web", "src", "generated", "api-client.ts")
	goPath := filepath.Join(root, "internal", "generated", "contracts.go")
	tsPath := filepath.Join(root, "web", "src", "generated", "contracts.ts")
	modelsPath := filepath.Join(root, "web", "src", "generated", "models.ts")
	if *check {
		checkFile(goPath, goOutput)
		checkFile(tsPath, tsOutput)
		checkFile(clientPath, clientOutput)
		checkFile(modelsPath, modelsOutput)
		return
	}
	writeFile(goPath, goOutput)
	writeFile(tsPath, tsOutput)
	writeFile(clientPath, clientOutput)
	writeFile(modelsPath, modelsOutput)
	fmt.Printf("generated %s\ngenerated %s\ngenerated %s\ngenerated %s\n", goPath, tsPath, clientPath, modelsPath)
}

func loadSources(root string) (sourceData, []string, error) {
	paths := []string{
		filepath.Join(root, "contracts", "openapi", "openapi.json"),
		filepath.Join(root, "contracts", "error_codes.yaml"),
		filepath.Join(root, "contracts", "event_types.yaml"),
		filepath.Join(root, "contracts", "user_status_mapping.yaml"),
		filepath.Join(root, "contracts", "state_enums.yaml"),
	}
	contents := make([]string, len(paths))
	for index, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return sourceData{}, nil, err
		}
		contents[index] = normalizeContractSource(content)
	}
	var openAPI map[string]any
	if err := json.Unmarshal([]byte(contents[0]), &openAPI); err != nil {
		return sourceData{}, nil, fmt.Errorf("decode OpenAPI: %w", err)
	}
	return sourceData{
		openAPI:    openAPI,
		errors:     parseCodes(contents[1]),
		retryable:  parseRetryable(contents[1]),
		events:     parseEvents(contents[2]),
		userStatus: parseStatuses(contents[3]),
		states:     parseStates(contents[4]),
	}, contents, nil
}

func normalizeContractSource(content []byte) string {
	return strings.ReplaceAll(string(content), "\r\n", "\n")
}

func parseCodes(content string) []string {
	var result []string
	for _, line := range strings.Split(content, "\n") {
		match := codePattern.FindStringSubmatch(line)
		if len(match) == 2 && match[1] != "SCHEMA_VERSION" {
			result = append(result, match[1])
		}
	}
	return sortedUnique(result)
}

// parseRetryable 提取每个错误码的 retryable 冻结值。
func parseRetryable(content string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		match := retryablePattern.FindStringSubmatch(line)
		if len(match) == 3 {
			result[match[1]] = match[2]
		}
	}
	return result
}

func parseEvents(content string) []string {
	var result []string
	inEvents := false
	for _, line := range strings.Split(content, "\n") {
		if line == "events:" {
			inEvents = true
			continue
		}
		if inEvents && line != "" && !strings.HasPrefix(line, "  ") {
			break
		}
		if inEvents {
			match := eventPattern.FindStringSubmatch(line)
			if len(match) == 2 {
				result = append(result, match[1])
			}
		}
	}
	return sortedUnique(result)
}

func parseStatuses(content string) map[string]string {
	result := make(map[string]string)
	inLabels := false
	for _, line := range strings.Split(content, "\n") {
		if line == "visible_labels:" {
			inLabels = true
			continue
		}
		if inLabels && line != "" && !strings.HasPrefix(line, "  ") {
			break
		}
		if inLabels {
			match := statusPattern.FindStringSubmatch(line)
			if len(match) == 3 {
				result[match[1]] = strings.TrimSpace(match[2])
			}
		}
	}
	return result
}

func parseStates(content string) map[string][]string {
	result := make(map[string][]string)
	currentDomain := ""
	for _, line := range strings.Split(content, "\n") {
		if match := yamlDomainPattern.FindStringSubmatch(line); len(match) == 2 {
			currentDomain = match[1]
			continue
		}
		if currentDomain == "" {
			continue
		}
		if match := yamlValuesPattern.FindStringSubmatch(line); len(match) == 2 {
			values := strings.Split(match[1], ",")
			for index := range values {
				values[index] = strings.TrimSpace(values[index])
			}
			result[currentDomain] = sortedUnique(values)
		}
	}
	return result
}

func renderGo(data sourceData, sourceDigest string) string {
	operations := operationIDs(data.openAPI)
	var builder strings.Builder
	builder.WriteString("// Code generated by tools/codegen; DO NOT EDIT.\n")
	builder.WriteString("package generated\n\n")
	builder.WriteString("const SourceDigest = \"")
	builder.WriteString(sourceDigest)
	builder.WriteString("\"\n\n")
	writeStringSlice(&builder, "OperationIDs", operations)
	writeStringSlice(&builder, "ErrorCodes", data.errors)
	writeStringSlice(&builder, "EventTypes", data.events)
	writeStringSlice(&builder, "UserStatusIDs", sortedMapKeys(data.userStatus))
	for _, domain := range sortedMapKeysSlice(data.states) {
		writeStringSlice(&builder, goIdentifier(domain), data.states[domain])
	}
	return strings.TrimRight(builder.String(), "\n") + "\n"
}

func renderTypeScript(data sourceData, sourceDigest string) string {
	operations := operationIDs(data.openAPI)
	var builder strings.Builder
	builder.WriteString("// Code generated by tools/codegen; DO NOT EDIT.\n")
	builder.WriteString("export const SOURCE_DIGEST = \"")
	builder.WriteString(sourceDigest)
	builder.WriteString("\" as const;\n\n")
	writeTypeScriptSlice(&builder, "OPERATION_IDS", operations)
	writeTypeScriptSlice(&builder, "ERROR_CODES", data.errors)
	writeTypeScriptMap(&builder, "ERROR_RETRYABLE", data.retryable)
	writeTypeScriptSlice(&builder, "EVENT_TYPES", data.events)
	writeTypeScriptSlice(&builder, "USER_STATUS_IDS", sortedMapKeys(data.userStatus))
	for _, domain := range sortedMapKeysSlice(data.states) {
		writeTypeScriptSlice(&builder, strings.ToUpper(domain), data.states[domain])
	}
	return builder.String()
}

// writeTypeScriptMap 输出 Record<string, boolean> 形式的冻结映射。
func writeTypeScriptMap(builder *strings.Builder, name string, values map[string]string) {
	builder.WriteString("export const " + name + ": Record<string, boolean> = {\n")
	for _, key := range sortedMapKeys(values) {
		builder.WriteString("  \"" + key + "\": " + values[key] + ",\n")
	}
	builder.WriteString("} as const;\n\n")
}
func operationIDs(document map[string]any) []string {
	paths, ok := document["paths"].(map[string]any)
	if !ok {
		return nil
	}
	var result []string
	for _, rawPath := range paths {
		pathItem, ok := rawPath.(map[string]any)
		if !ok {
			continue
		}
		for _, method := range []string{"get", "post", "put", "delete", "patch"} {
			operation, ok := pathItem[method].(map[string]any)
			if !ok {
				continue
			}
			if operationID, ok := operation["operationId"].(string); ok {
				result = append(result, operationID)
			}
		}
	}
	return sortedUnique(result)
}

func writeStringSlice(builder *strings.Builder, name string, values []string) {
	builder.WriteString("var ")
	builder.WriteString(name)
	builder.WriteString(" = []string{\n")
	for _, value := range values {
		builder.WriteString("\t\"")
		builder.WriteString(value)
		builder.WriteString("\",\n")
	}
	builder.WriteString("}\n\n")
}

func writeTypeScriptSlice(builder *strings.Builder, name string, values []string) {
	builder.WriteString("export const ")
	builder.WriteString(name)
	builder.WriteString(" = [\n")
	for _, value := range values {
		builder.WriteString("  \"")
		builder.WriteString(value)
		builder.WriteString("\",\n")
	}
	builder.WriteString("] as const;\n\n")
}

func sortedMapKeys(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	return sortedUnique(result)
}

func sortedMapKeysSlice(values map[string][]string) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func goIdentifier(value string) string {
	parts := strings.Split(value, "_")
	for index, part := range parts {
		if part == "" {
			continue
		}
		parts[index] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "") + "Values"
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func digest(sources []string) string {
	hash := sha256.New()
	for _, source := range sources {
		hash.Write([]byte(strconv.Itoa(len(source))))
		hash.Write([]byte{0})
		hash.Write([]byte(source))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func writeFile(path, content string) {
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		fatal(err)
	}
}

func checkFile(path, expected string) {
	actual, err := os.ReadFile(path)
	if err != nil {
		fatal(fmt.Errorf("read generated file %s: %w", path, err))
	}
	if string(actual) != expected {
		fatal(fmt.Errorf("generated file is stale: %s; run make generate", path))
	}
	fmt.Printf("checked %s\n", path)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// clientOperation 是一个 OpenAPI 操作的客户端方法描述。
type clientOperation struct {
	OperationID string
	Method      string
	Path        string
	PathParams  []string
	HasBody     bool
	IsStream    bool
}

func clientOperations(document map[string]any) []clientOperation {
	paths, ok := document["paths"].(map[string]any)
	if !ok {
		return nil
	}
	result := []clientOperation{}
	for rawPath, rawItem := range paths {
		pathItem, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		for _, method := range []string{"get", "post", "put", "delete", "patch"} {
			operation, ok := pathItem[method].(map[string]any)
			if !ok {
				continue
			}
			operationID, ok := operation["operationId"].(string)
			if !ok {
				continue
			}
			entry := clientOperation{OperationID: operationID, Method: strings.ToUpper(method), Path: rawPath}
			if strings.Contains(rawPath, "events/stream") {
				entry.IsStream = true
			}
			if method != "get" && method != "delete" {
				entry.HasBody = true
			}
			start := 0
			for {
				open := strings.Index(rawPath[start:], "{")
				if open < 0 {
					break
				}
				open += start
				close_ := strings.Index(rawPath[open:], "}")
				if close_ < 0 {
					break
				}
				entry.PathParams = append(entry.PathParams, rawPath[open+1:open+close_])
				start = open + close_ + 1
			}
			result = append(result, entry)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].OperationID < result[j].OperationID })
	return result
}

// renderClient 生成 web/src/generated/api-client.ts：每个 OpenAPI 操作一个
// 强类型方法（HTTP 动词与路径内嵌，业务代码不得手写 endpoint 字符串）、
// 合同错误解码器与 cursor 辅助函数。
func renderClient(document map[string]any) (string, error) {
	operations := clientOperations(document)
	var builder strings.Builder
	builder.WriteString("// Code generated by tools/codegen; DO NOT EDIT.\n")
	builder.WriteString("import { ERROR_CODES, ERROR_RETRYABLE } from \"./contracts\";\n\n")
	builder.WriteString("export interface ApiErrorBody {\n  code: string;\n  message: string;\n  request_id: string;\n  retryable: boolean;\n  details?: Record<string, unknown>;\n}\n\n")
	builder.WriteString("export class ApiRequestError extends Error {\n  constructor(readonly status: number, readonly body: ApiErrorBody) {\n    super(body.message);\n    this.name = \"ApiRequestError\";\n  }\n}\n\n")
	builder.WriteString("export interface RequestOptions {\n  signal?: AbortSignal;\n  query?: Record<string, string | undefined>;\n  body?: unknown;\n  headers?: Record<string, string>;\n}\n\n")
	builder.WriteString("export type JsonObject = Record<string, unknown>;\n\n")
	builder.WriteString("export class ApiClient {\n  constructor(private baseUrl: string, private fetchImpl: typeof fetch = fetch) {}\n\n")
	builder.WriteString("  private buildUrl(path: string, query?: Record<string, string | undefined>): string {\n    const url = new URL(this.baseUrl + path);\n    for (const [key, value] of Object.entries(query ?? {})) {\n      if (value !== undefined) {\n        url.searchParams.set(key, value);\n      }\n    }\n    return url.toString();\n  }\n\n")
	builder.WriteString("  private async request<T = JsonObject>(path: string, init: RequestInit, query?: Record<string, string | undefined>): Promise<T> {\n    const response = await this.fetchImpl.call(globalThis, this.buildUrl(path, query), init);\n    const text = await response.text();\n    const parsed: unknown = text ? JSON.parse(text) : {};\n    if (!response.ok) {\n      throw decodeError(response.status, parsed);\n    }\n    return parsed as T;\n  }\n")
	for _, operation := range operations {
		params := []string{}
		for _, param := range operation.PathParams {
			params = append(params, param+": string")
		}
		args := strings.Join(params, ", ")
		callPath := serverBasePath(document) + operation.Path
		for _, param := range operation.PathParams {
			callPath = strings.ReplaceAll(callPath, "{"+param+"}", "${encodeURIComponent("+param+")}")
		}
		callPath = "`" + callPath + "`"
		if args != "" {
			args += ", "
		}
		query := ""
		init := "{ method: \"" + operation.Method + "\""
		if operation.HasBody {
			init += ", body: JSON.stringify(opts?.body ?? {}), headers: {\"Content-Type\": \"application/json\", ...(opts?.headers ?? {})}"
		} else {
			init += ", headers: opts?.headers"
		}
		init += ", signal: opts?.signal }"
		if !operation.IsStream {
			query = " opts?.query"
			if args != "" {
				args += "opts?: RequestOptions"
			} else {
				args = "opts?: RequestOptions"
			}
			fmt.Fprintf(&builder, "  async %s<T = JsonObject>(%s): Promise<T> {\n    return this.request<T>(%s, %s,%s);\n  }\n\n", operation.OperationID, args, callPath, init, query)
		} else {
			if args != "" {
				args += "opts?: RequestOptions"
			} else {
				args = "opts?: RequestOptions"
			}
			fmt.Fprintf(&builder, "  async %s(%s): Promise<Response> {\n    return this.fetchImpl.call(globalThis, this.buildUrl(%s, opts?.query), %s);\n  }\n\n", operation.OperationID, args, callPath, init)
		}
	}
	builder.WriteString("}\n\n")
	builder.WriteString(`// decodeError 只按合同错误码分支；未知码归一化为 INTERNAL_ERROR。
export function decodeError(status: number, parsed: unknown): ApiRequestError {
  const envelope = (parsed ?? {}) as { error?: Partial<ApiErrorBody> };
  const raw = envelope.error ?? {};
  const known = typeof raw.code === "string" && (ERROR_CODES as readonly string[]).includes(raw.code);
  const code = known ? (raw.code as string) : "INTERNAL_ERROR";
  const body: ApiErrorBody = {
    code,
    message: typeof raw.message === "string" ? raw.message : "",
    request_id: typeof raw.request_id === "string" ? raw.request_id : "",
    retryable: known ? ERROR_RETRYABLE[code] === true : false,
    details: raw.details as Record<string, unknown> | undefined,
  };
  return new ApiRequestError(status, body);
}

// cursor 辅助：opaque cursor 由服务端签发，客户端只透传。
export function withCursor(query: Record<string, string | undefined> = {}, cursor?: string, limit?: number): Record<string, string | undefined> {
  const next = { ...query };
  if (cursor !== undefined) {
    next.cursor = cursor;
  }
  if (limit !== undefined) {
    next.limit = String(limit);
  }
  return next;
}
`)
	return builder.String(), nil
}

// serverBasePath 从 OpenAPI servers[0].url 提取 API 基础路径（如 /api/v1）。
func serverBasePath(document map[string]any) string {
	servers, ok := document["servers"].([]any)
	if !ok || len(servers) == 0 {
		return ""
	}
	server, ok := servers[0].(map[string]any)
	if !ok {
		return ""
	}
	url, _ := server["url"].(string)
	return strings.TrimSuffix(url, "/")
}
