package instabox

import (
	"encoding/json"
	"errors"
)

// MarshalJSON encodes either all proxy secrets or an explicit selection.
func (selection SecretSelection) MarshalJSON() ([]byte, error) {
	if selection.All {
		return json.Marshal("all")
	}
	if selection.Names == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(selection.Names)
}

// UnmarshalJSON decodes the specification's string-or-array union.
func (selection *SecretSelection) UnmarshalJSON(raw []byte) error {
	var mode string
	if json.Unmarshal(raw, &mode) == nil {
		if mode != "all" {
			return errors.New("instabox: invalid secret selection")
		}
		*selection = SecretSelection{All: true}
		return nil
	}
	var names []string
	if string(raw) == "null" || json.Unmarshal(raw, &names) != nil {
		return errors.New("instabox: invalid secret selection")
	}
	*selection = SecretSelection{Names: names}
	return nil
}
