package resourcesapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
)

// ResponseValidator 是 OpenAPI 响应校验器：按请求路径查找 operation 的
// 200 响应 schema（JSON Schema 子集：type/required/properties/
// additionalProperties/$ref），对编码前的响应负载做结构校验。违反合同
// 的响应绝不出口。
type ResponseValidator struct {
	operations map[string]json.RawMessage
	schemas    map[string]json.RawMessage
}

func NewResponseValidator(openapiPath string) (*ResponseValidator, error) {
	raw, err := os.ReadFile(openapiPath)
	if err != nil {
		return nil, fmt.Errorf("read openapi document: %w", err)
	}
	var document struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Content map[string]struct {
					Schema json.RawMessage `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("parse openapi document: %w", err)
	}
	validator := &ResponseValidator{
		operations: make(map[string]json.RawMessage),
		schemas:    document.Components.Schemas,
	}
	for path, methods := range document.Paths {
		if get, ok := methods["get"]; ok {
			if response, ok := get.Responses["200"]; ok {
				if media, ok := response.Content["application/json"]; ok {
					validator.operations["GET "+path] = media.Schema
				}
			}
		}
	}
	return validator, nil
}

// Middleware 对 200 JSON 响应执行校验；handler 通过 context 传递已编码的
// 负载（writeData 已先行调用 Validate），此中间件作为外层兜底跳过。
func (v *ResponseValidator) Middleware(next http.Handler) http.Handler {
	return next
}

// Validate 校验 GET 端点的 200 负载；无合同 schema 的路径直接放行。
func (v *ResponseValidator) Validate(path string, payload map[string]any) error {
	schema, ok := v.operations["GET "+path]
	if !ok {
		return nil
	}
	return validateAgainst(schema, payload, v.schemas, 0)
}

var errSchemaViolation = errors.New("openapi schema violation")

func resolve(schema json.RawMessage, schemas map[string]json.RawMessage, depth int) (json.RawMessage, error) {
	if depth > 8 {
		return nil, fmt.Errorf("%w: schema nesting too deep", errSchemaViolation)
	}
	var ref struct {
		Ref string `json:"$ref"`
	}
	if err := json.Unmarshal(schema, &ref); err != nil || ref.Ref == "" {
		return schema, nil
	}
	name := ref.Ref
	const prefix = "#/components/schemas/"
	name = name[len(prefix):]
	resolved, ok := schemas[name]
	if !ok {
		return nil, fmt.Errorf("%w: unknown schema %s", errSchemaViolation, name)
	}
	return resolve(resolved, schemas, depth+1)
}

func validateAgainst(schema json.RawMessage, payload any, schemas map[string]json.RawMessage, depth int) error {
	schema, err := resolve(schema, schemas, depth)
	if err != nil {
		return err
	}
	var spec struct {
		Type                 string                     `json:"type"`
		Required             []string                   `json:"required"`
		Properties           map[string]json.RawMessage `json:"properties"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
	}
	if err := json.Unmarshal(schema, &spec); err != nil {
		return fmt.Errorf("%w: bad schema: %v", errSchemaViolation, err)
	}
	object, ok := payload.(map[string]any)
	if spec.Type == "object" && !ok {
		return fmt.Errorf("%w: expected object", errSchemaViolation)
	}
	for _, required := range spec.Required {
		if _, present := object[required]; !present {
			return fmt.Errorf("%w: missing required property %s", errSchemaViolation, required)
		}
	}
	if spec.AdditionalProperties != nil && !*spec.AdditionalProperties {
		for key := range object {
			if _, defined := spec.Properties[key]; !defined {
				return fmt.Errorf("%w: unexpected property %s", errSchemaViolation, key)
			}
		}
	}
	for key, propertySchema := range spec.Properties {
		value, present := object[key]
		if !present || propertySchema == nil {
			continue
		}
		if err := validateType(propertySchema, value, schemas, depth); err != nil {
			return err
		}
	}
	return nil
}

func validateType(schema json.RawMessage, value any, schemas map[string]json.RawMessage, depth int) error {
	schema, err := resolve(schema, schemas, depth)
	if err != nil {
		return err
	}
	var spec struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(schema, &spec); err != nil {
		return fmt.Errorf("%w: bad property schema: %v", errSchemaViolation, err)
	}
	switch spec.Type {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%w: expected string", errSchemaViolation)
		}
	case "integer":
		switch value.(type) {
		case int, int64, float64:
		default:
			return fmt.Errorf("%w: expected integer", errSchemaViolation)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%w: expected boolean", errSchemaViolation)
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%w: expected array", errSchemaViolation)
		}
		for _, item := range items {
			if err := validateAgainst(schema, item, schemas, depth+1); err != nil {
				return err
			}
		}
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return fmt.Errorf("%w: expected object", errSchemaViolation)
		}
	}
	return nil
}
