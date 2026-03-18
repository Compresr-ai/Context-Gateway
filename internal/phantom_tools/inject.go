package phantom_tools

import (
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// InjectPhantomTool appends a phantom tool to tools[], skipping if already present.
func InjectPhantomTool(body []byte, toolName string, toolJSON []byte) ([]byte, error) {
	if HasToolByName(body, toolName) {
		return body, nil
	}

	toolsResult := gjson.GetBytes(body, "tools")
	if !toolsResult.Exists() {
		return sjson.SetRawBytes(body, "tools", append(append([]byte{'['}, toolJSON...), ']'))
	}

	return sjson.SetRawBytes(body, "tools.-1", toolJSON)
}

// HasToolByName checks if a tool with the given name already exists in the tools[] array.
func HasToolByName(body []byte, name string) bool {
	toolsResult := gjson.GetBytes(body, "tools")
	if !toolsResult.Exists() {
		return false
	}

	found := false
	toolsResult.ForEach(func(_, value gjson.Result) bool {
		if value.Get("name").String() == name {
			found = true
			return false
		}
		if value.Get("function.name").String() == name {
			found = true
			return false
		}
		return true
	})
	return found
}

// RemoveToolByName removes a tool with the given name from the tools[] array.
// Returns the modified body and whether the tool was found and removed.
func RemoveToolByName(body []byte, name string) ([]byte, bool) {
	toolsResult := gjson.GetBytes(body, "tools")
	if !toolsResult.Exists() {
		return body, false
	}

	idx := -1
	toolsResult.ForEach(func(key, value gjson.Result) bool {
		if value.Get("name").String() == name || value.Get("function.name").String() == name {
			idx = int(key.Int())
			return false
		}
		return true
	})

	if idx < 0 {
		return body, false
	}

	result, err := sjson.DeleteBytes(body, "tools."+strconv.Itoa(idx))
	if err != nil {
		return body, false
	}
	return result, true
}
