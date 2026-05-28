package agui

const (
	JSONSchemaTypeObject  = "object"
	JSONSchemaTypeBoolean = "boolean"
	JSONSchemaTypeString  = "string"
)

type JSONSchemaProperties map[string]JSONSchema

func BooleanSchema() JSONSchema {
	return JSONSchema{"type": JSONSchemaTypeBoolean}
}

func StringSchema() JSONSchema {
	return JSONSchema{"type": JSONSchemaTypeString}
}

func ObjectSchema(properties JSONSchemaProperties, required ...string) JSONSchema {
	schema := JSONSchema{"type": JSONSchemaTypeObject}
	if len(properties) > 0 {
		props := make(JSONSchemaProperties, len(properties))
		for key, value := range properties {
			props[key] = value
		}
		schema["properties"] = props
	}
	if len(required) > 0 {
		schema["required"] = append([]string(nil), required...)
	}
	return schema
}
