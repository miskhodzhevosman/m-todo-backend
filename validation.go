package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type apiError struct {
	status int
	detail string
}

func (e *apiError) Error() string { return e.detail }

func invalid(detail string) error { return &apiError{http.StatusUnprocessableEntity, detail} }

var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func validUrgency(value string) bool {
	switch value {
	case "low", "medium", "high", "critical":
		return true
	}
	return false
}

// A map preserves the distinction between an omitted PATCH field and explicit null.
func readPayload(w http.ResponseWriter, r *http.Request, entity string, create bool) (map[string]any, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		return nil, invalid("Expected a JSON object")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, invalid("Expected a single JSON object")
	}
	allowed := []string{"name", "color"}
	if entity == "todos" {
		allowed = []string{"title", "description", "urgency", "due_at", "category_id"}
		if !create {
			allowed = append(allowed, "completed")
		}
	}
	data := make(map[string]any)
	for _, field := range allowed {
		value, exists := raw[field]
		if !exists {
			continue
		}
		if string(value) == "null" {
			switch field {
			case "description", "due_at", "category_id":
				data[field] = nil
				continue
			default:
				return nil, invalid(field + " cannot be null")
			}
		}
		switch field {
		case "category_id":
			var id int64
			if err := json.Unmarshal(value, &id); err != nil {
				return nil, invalid("category_id must be an integer")
			}
			data[field] = id
		case "completed":
			var completed bool
			if err := json.Unmarshal(value, &completed); err != nil {
				return nil, invalid("completed must be a boolean")
			}
			data[field] = completed
		default:
			var text string
			if err := json.Unmarshal(value, &text); err != nil {
				return nil, invalid(field + " must be a string")
			}
			switch field {
			case "name", "title":
				max := 200
				if field == "name" {
					max = 50
				}
				if n := utf8.RuneCountInString(text); n < 1 || n > max || strings.TrimSpace(text) == "" {
					return nil, invalid(fmt.Sprintf("%s must contain 1 to %d characters", field, max))
				}
				if create {
					text = strings.TrimSpace(text)
				}
			case "color":
				if !colorPattern.MatchString(text) {
					return nil, invalid("color must be a hex color, for example #6366f1")
				}
			case "urgency":
				if !validUrgency(text) {
					return nil, invalid("urgency must be low, medium, high or critical")
				}
			case "due_at":
				var err error
				text, err = normalizeDateTime(text)
				if err != nil {
					return nil, invalid("due_at must be an ISO 8601 datetime")
				}
			}
			data[field] = text
		}
	}
	if create {
		required := "title"
		if entity == "categories" {
			required = "name"
		}
		if _, ok := data[required]; !ok {
			return nil, invalid(required + " is required")
		}
		if entity == "categories" {
			if _, ok := data["color"]; !ok {
				data["color"] = "#6366f1"
			}
		} else if _, ok := data["urgency"]; !ok {
			data["urgency"] = "medium"
		}
	} else if len(data) == 0 {
		return nil, &apiError{http.StatusBadRequest, "No fields to update"}
	}
	return data, nil
}

func normalizeDateTime(value string) (string, error) {
	for _, layout := range []string{
		time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02T15:04",
		"2006-01-02 15:04:05Z07:00", "2006-01-02 15:04:05", "2006-01-02",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			if strings.Contains(layout, "Z07:00") {
				return parsed.Format("2006-01-02T15:04:05.999999999-07:00"), nil
			}
			return parsed.Format("2006-01-02T15:04:05.999999999"), nil
		}
	}
	return "", errors.New("invalid datetime")
}
