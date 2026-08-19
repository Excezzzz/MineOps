// Package util — общие вспомогательные функции.
package util

import (
	"encoding/json"
	"fmt"
)

// ToInt конвертирует any-значение (JSON-дерево) в int; 0 при неудаче.
func ToInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		var n int
		fmt.Sscanf(t, "%d", &n)
		return n
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	}
	return 0
}

// ToStr конвертирует any-значение в string; "" при неудаче.
func ToStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// ToStrList конвертирует any-массив в []string (строковые элементы).
func ToStrList(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// FirstNonEmpty возвращает первую непустую строку из переданных.
func FirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}