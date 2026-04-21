package ptytest

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/DocumentDrivenDX/agent/internal/pty/cassette"
	"github.com/DocumentDrivenDX/agent/internal/pty/session"
)

func evaluateAssertion(group string, spec AssertionSpec, events []cassette.Event) *Failure {
	if len(events) == 0 {
		return failure(group, spec, events, 0, noEventsError().Error())
	}
	switch spec.Type {
	case "frame_contains":
		if frame := findFrame(events, spec.AtMS, func(fr *cassette.FrameRecord) bool { return frameContains(fr, spec.Text) }); frame != nil {
			return nil
		}
		return failure(group, spec, events, targetMS(spec), fmt.Sprintf("frame containing %q not found", spec.Text))
	case "frame_never_contains":
		if frame := findFrame(events, nil, func(fr *cassette.FrameRecord) bool { return frameContains(fr, spec.Text) }); frame != nil {
			return failure(group, spec, events, frame.TMS, fmt.Sprintf("frame unexpectedly contained %q", spec.Text))
		}
		return nil
	case "frame_eventually_contains":
		end := targetMS(spec) + spec.WithinMS
		if spec.WithinMS == 0 {
			end = events[len(events)-1].TMS
		}
		if frame := findFrameInWindow(events, targetMS(spec), end, func(fr *cassette.FrameRecord) bool { return frameContains(fr, spec.Text) }); frame != nil {
			return nil
		}
		return failure(group, spec, events, targetMS(spec), fmt.Sprintf("frame did not eventually contain %q", spec.Text))
	case "frame_stable_for":
		if spec.StableForMS <= 0 {
			return failure(group, spec, events, targetMS(spec), "stable_for_ms must be greater than zero")
		}
		if stableFrameText(events, spec.Text, targetMS(spec), spec.StableForMS) {
			return nil
		}
		return failure(group, spec, events, targetMS(spec), fmt.Sprintf("frame %q was not stable for %dms", spec.Text, spec.StableForMS))
	case "cursor":
		frame := nearestFrame(events, targetMS(spec))
		if frame == nil {
			return failure(group, spec, events, targetMS(spec), "no frame for cursor assertion")
		}
		if frame.Cursor.Row != spec.CursorRow || frame.Cursor.Col != spec.CursorCol {
			return failure(group, spec, events, frame.TMS, fmt.Sprintf("cursor got=%d,%d want=%d,%d", frame.Cursor.Row, frame.Cursor.Col, spec.CursorRow, spec.CursorCol))
		}
		if spec.CursorVisible != nil && frame.Cursor.Visible != *spec.CursorVisible {
			return failure(group, spec, events, frame.TMS, fmt.Sprintf("cursor visible got=%v want=%v", frame.Cursor.Visible, *spec.CursorVisible))
		}
		return nil
	case "size":
		frame := nearestFrame(events, targetMS(spec))
		if frame == nil {
			return failure(group, spec, events, targetMS(spec), "no frame for size assertion")
		}
		if frame.Size.Rows != spec.Rows || frame.Size.Cols != spec.Cols {
			return failure(group, spec, events, frame.TMS, fmt.Sprintf("size got=%dx%d want=%dx%d", frame.Size.Rows, frame.Size.Cols, spec.Rows, spec.Cols))
		}
		return nil
	case "size_eventually":
		end := targetMS(spec) + spec.WithinMS
		if spec.WithinMS == 0 {
			end = events[len(events)-1].TMS
		}
		if frame := findFrameInWindow(events, targetMS(spec), end, func(fr *cassette.FrameRecord) bool {
			return fr.Size.Rows == spec.Rows && fr.Size.Cols == spec.Cols
		}); frame != nil {
			return nil
		}
		return failure(group, spec, events, targetMS(spec), fmt.Sprintf("size %dx%d not found", spec.Rows, spec.Cols))
	case "style":
		frame := nearestFrame(events, targetMS(spec))
		if frame == nil {
			return failure(group, spec, events, targetMS(spec), "no frame for style assertion")
		}
		if !styleFound(frame, spec.Text, spec.FG, spec.BG) {
			return failure(group, spec, events, frame.TMS, fmt.Sprintf("style not found for %q", spec.Text))
		}
		return nil
	case "raw_output_order":
		if outputOrdered(events) {
			return nil
		}
		return failure(group, spec, events, targetMS(spec), "raw output chunks are out of seq/t_ms/offset order")
	case "input_bytes", "paste_boundary":
		want, err := decodeHex(spec.BytesHex)
		if err != nil {
			return failure(group, spec, events, targetMS(spec), err.Error())
		}
		if inputFound(events, want, session.Key(spec.Key)) {
			return nil
		}
		return failure(group, spec, events, targetMS(spec), "input bytes/key not found")
	case "resize_order":
		if kindBeforeAfter(events, cassette.EventInput, spec.BeforeKind, spec.AfterKind) {
			return nil
		}
		return failure(group, spec, events, targetMS(spec), "resize ordering assertion failed")
	case "service_json":
		for _, ev := range events {
			if ev.Service == nil {
				continue
			}
			got, ok := matchJSONPath(ev.Service.Payload, spec.JSONPath)
			if ok && assertEqual(got, spec.Equals) {
				return nil
			}
		}
		return failure(group, spec, events, targetMS(spec), fmt.Sprintf("service JSON path %q did not equal %v", spec.JSONPath, spec.Equals))
	case "final_metadata":
		final := finalEvent(events)
		if final == nil {
			return failure(group, spec, events, targetMS(spec), "final event missing")
		}
		if spec.MetadataKey != "" && !assertEqual(final.Metadata[spec.MetadataKey], spec.Equals) {
			return failure(group, spec, events, final.TMS, fmt.Sprintf("metadata %q got=%v want=%v", spec.MetadataKey, final.Metadata[spec.MetadataKey], spec.Equals))
		}
		if spec.Text != "" && !strings.Contains(final.FinalText, spec.Text) {
			return failure(group, spec, events, final.TMS, fmt.Sprintf("final text %q does not contain %q", final.FinalText, spec.Text))
		}
		return nil
	case "exit_status", "timeout_cancel_status":
		final := finalEvent(events)
		if final == nil || final.Exit == nil {
			return failure(group, spec, events, targetMS(spec), "final exit status missing")
		}
		if spec.ExitCode != nil && final.Exit.Code != *spec.ExitCode {
			return failure(group, spec, events, final.TMS, fmt.Sprintf("exit code got=%d want=%d", final.Exit.Code, *spec.ExitCode))
		}
		if spec.Signaled != nil && final.Exit.Signaled != *spec.Signaled {
			return failure(group, spec, events, final.TMS, fmt.Sprintf("signaled got=%v want=%v", final.Exit.Signaled, *spec.Signaled))
		}
		return nil
	case "timing_gap":
		if timingGapOK(events, spec.Kind, spec.MinMS, spec.MaxMS) {
			return nil
		}
		return failure(group, spec, events, targetMS(spec), "timing gap assertion failed")
	default:
		return failure(group, spec, events, targetMS(spec), fmt.Sprintf("unknown assertion type %q", spec.Type))
	}
}

func targetMS(spec AssertionSpec) int64 {
	if spec.AtMS == nil {
		return 0
	}
	return *spec.AtMS
}

func frameContains(frame *cassette.FrameRecord, text string) bool {
	return strings.Contains(strings.Join(frame.Text, "\n"), text)
}

func findFrame(events []cassette.Event, at *int64, pred func(*cassette.FrameRecord) bool) *cassette.FrameRecord {
	if at != nil {
		frame := nearestFrame(events, *at)
		if frame != nil && pred(frame) {
			return frame
		}
		return nil
	}
	for _, ev := range events {
		if ev.Frame == nil {
			continue
		}
		if pred(ev.Frame) {
			return ev.Frame
		}
	}
	return nil
}

func findFrameInWindow(events []cassette.Event, start, end int64, pred func(*cassette.FrameRecord) bool) *cassette.FrameRecord {
	for _, ev := range events {
		if ev.Frame != nil && ev.TMS >= start && ev.TMS <= end && pred(ev.Frame) {
			return ev.Frame
		}
	}
	return nil
}

func nearestFrame(events []cassette.Event, ms int64) *cassette.FrameRecord {
	var best *cassette.FrameRecord
	bestDelta := int64(1<<62 - 1)
	for _, ev := range events {
		if ev.Frame == nil {
			continue
		}
		delta := ev.TMS - ms
		if delta < 0 {
			delta = -delta
		}
		if delta < bestDelta {
			best = ev.Frame
			bestDelta = delta
		}
	}
	return best
}

func stableFrameText(events []cassette.Event, text string, start, duration int64) bool {
	end := start + duration
	seen := false
	for _, ev := range events {
		if ev.Frame == nil || ev.TMS < start || ev.TMS > end {
			continue
		}
		seen = true
		if !frameContains(ev.Frame, text) {
			return false
		}
	}
	return seen
}

func styleFound(frame *cassette.FrameRecord, text string, fg, bg *uint32) bool {
	want := []rune(text)
	if len(want) == 0 {
		return false
	}
	for _, row := range frame.Cells {
		if len(row) < len(want) {
			continue
		}
		for start := 0; start <= len(row)-len(want); start++ {
			matched := true
			for i, ch := range want {
				cell := row[start+i]
				if cell.Char != ch {
					matched = false
					break
				}
				if fg != nil && cell.FG != *fg {
					matched = false
					break
				}
				if bg != nil && cell.BG != *bg {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
			return true
		}
	}
	return false
}

func outputOrdered(events []cassette.Event) bool {
	var lastSeq uint64
	var lastTMS, lastOffset int64 = -1, -1
	first := true
	for _, ev := range events {
		if ev.Output == nil {
			continue
		}
		if !first && (ev.Seq <= lastSeq || ev.TMS < lastTMS || ev.Output.Offset < lastOffset) {
			return false
		}
		first = false
		lastSeq = ev.Seq
		lastTMS = ev.TMS
		lastOffset = ev.Output.Offset
	}
	return true
}

func inputFound(events []cassette.Event, b []byte, key session.Key) bool {
	for _, ev := range events {
		if ev.Input == nil {
			continue
		}
		if len(b) > 0 && bytes.Equal(ev.Input.Bytes, b) {
			return true
		}
		if key != "" && ev.Input.Key == key {
			return true
		}
	}
	return false
}

func kindBeforeAfter(events []cassette.Event, kind cassette.EventKind, before, after string) bool {
	var sawResize bool
	for _, ev := range events {
		if ev.Kind != kind || ev.Input == nil || ev.Input.Kind != session.EventResize {
			continue
		}
		sawResize = true
		var beforeOK, afterOK bool
		for _, other := range events {
			if before != "" && string(other.Kind) == before && other.Seq < ev.Seq {
				beforeOK = true
			}
			if after != "" && string(other.Kind) == after && other.Seq > ev.Seq {
				afterOK = true
			}
		}
		if before != "" && !beforeOK {
			return false
		}
		if after != "" && !afterOK {
			return false
		}
	}
	return sawResize
}

func finalEvent(events []cassette.Event) *cassette.FinalRecord {
	for _, ev := range events {
		if ev.Final != nil {
			return ev.Final
		}
	}
	return nil
}

func timingGapOK(events []cassette.Event, kind string, minMS, maxMS *int64) bool {
	var prev *cassette.Event
	var seen int
	hasConstraint := minMS != nil || maxMS != nil
	for i := range events {
		ev := events[i]
		if kind != "" && string(ev.Kind) != kind {
			continue
		}
		seen++
		if prev != nil {
			gap := ev.TMS - prev.TMS
			if minMS != nil && gap < *minMS {
				return false
			}
			if maxMS != nil && gap > *maxMS {
				return false
			}
		}
		prev = &events[i]
	}
	if hasConstraint && seen < 2 {
		return false
	}
	return seen > 0
}

func failure(group string, spec AssertionSpec, events []cassette.Event, ms int64, msg string) *Failure {
	return &Failure{
		Group:          group,
		Assertion:      spec,
		Message:        msg,
		NearestTMS:     ms,
		ScreenExcerpt:  screenExcerpt(events, ms),
		ServiceContext: serviceContext(events, ms),
	}
}

func screenExcerpt(events []cassette.Event, ms int64) string {
	frame := nearestFrame(events, ms)
	if frame == nil {
		return "<no frame>"
	}
	return strings.Join(frame.Text, "\n")
}

func serviceContext(events []cassette.Event, ms int64) string {
	var best *cassette.ServiceEventRecord
	bestDelta := int64(1<<62 - 1)
	for _, ev := range events {
		if ev.Service == nil {
			continue
		}
		delta := ev.TMS - ms
		if delta < 0 {
			delta = -delta
		}
		if delta < bestDelta {
			best = ev.Service
			bestDelta = delta
		}
	}
	if best == nil {
		return "<no service event>"
	}
	return string(best.Payload)
}
