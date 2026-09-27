package spool

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

const stateFileName = "state.json"

// spoolState is state.json's shape: the next segment number to allocate, and the highest segment number this
// node has locally recorded as acknowledged by the controller (D24). It is written before the side effect it
// commits to (creating a segment file, or deleting an acknowledged one) ever happens, so a crash between the
// write and the side effect always leaves the side effect safe to retry or skip on restart.
type spoolState struct {
	NextSegment uint64 `json:"nextSegment"`
	AckedUpTo   uint64 `json:"ackedUpTo"`
}

// loadState reads state.json, defaulting to a fresh spool (segment numbering starts at 1, so 0 unambiguously
// means "no segment yet" wherever a segment number is reported).
func loadState(dir string) (spoolState, error) {
	data, err := os.ReadFile(filepath.Join(dir, stateFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return spoolState{NextSegment: 1}, nil
	}
	if err != nil {
		return spoolState{}, err
	}
	var state spoolState
	if err := json.Unmarshal(data, &state); err != nil {
		return spoolState{}, err
	}
	if state.NextSegment == 0 {
		state.NextSegment = 1
	}
	return state, nil
}

func saveState(dir string, state spoolState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, stateFileName), data)
}
