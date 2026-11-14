package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/domain"
	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/media"
	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/storage"
)

const (
	programWidth      = 96
	programHeight     = 54
	programFPS        = 24
	programFrames     = 24
	programSampleRate = 48000
)

// runProgramCheck exercises the multi-segment conform program end to end:
// distinct sync bases per segment, one cumulative sample timeline, exact
// segment traceability, and the guarantee that a failed program never exposes
// a partial bundle.
func runProgramCheck(ctx *checkContext) error {
	takes := []struct {
		takeID       string
		analysisID   string
		audioEventMS int
		offsetMS     int
	}{
		{takeID: "program-take-a", analysisID: "program-analysis-a", audioEventMS: 500, offsetMS: 125},
		{takeID: "program-take-b", analysisID: "program-analysis-b", audioEventMS: 250, offsetMS: -125},
		{takeID: "program-take-c", analysisID: "program-analysis-c", audioEventMS: 375, offsetMS: 0},
	}
	for _, take := range takes {
		audio, video, err := media.CalibrationTake(media.CalibrationSpec{
			SampleRate:      programSampleRate,
			Channels:        1,
			Frames:          programFrames,
			FPS:             programFPS,
			Width:           programWidth,
			Height:          programHeight,
			AudioEventMS:    take.audioEventMS,
			VideoEventFrame: 9,
			PeakAmplitude:   12000,
		})
		if err != nil {
			return err
		}
		if err := admitCustom(ctx, take.takeID, audio, video); err != nil {
			return err
		}
		if err := analyzeCustom(ctx, take.takeID, take.analysisID, take.offsetMS); err != nil {
			return err
		}
	}

	if err := verifyJoinedProgram(ctx, takes); err != nil {
		return err
	}
	if err := verifySplitDeterminism(ctx); err != nil {
		return err
	}
	if err := verifyProgramFailures(ctx); err != nil {
		return err
	}
	return nil
}

func verifyJoinedProgram(
	ctx *checkContext,
	takes []struct {
		takeID       string
		analysisID   string
		audioEventMS int
		offsetMS     int
	},
) error {
	segments := make([]map[string]any, 0, len(takes))
	for _, take := range takes {
		segments = append(segments, map[string]any{"analysis_id": take.analysisID})
	}
	response, body, err := ctx.requestJSON(http.MethodPost, "/v1/conform-programs", map[string]any{
		"program_id":        "program-joined-01",
		"max_correction_ms": 500,
		"segments":          segments,
	})
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusCreated {
		return fmt.Errorf("program create returned %d: %s", response.StatusCode, body)
	}
	var program domain.ConformProgram
	if err := json.Unmarshal(body, &program); err != nil {
		return fmt.Errorf("decode program: %w", err)
	}
	if program.State != domain.ConformStateCompleted || program.BundleDigest == "" {
		return fmt.Errorf("program did not complete: %s", program.ErrorMessage)
	}
	if program.TotalFrames != programFrames*len(takes) {
		return fmt.Errorf("program has %d frames, want %d", program.TotalFrames, programFrames*len(takes))
	}
	// 24 fps against 48 kHz is exactly 2000 samples per frame, so three
	// 24-frame segments must total 144000 audio frames on the shared timeline.
	if program.TotalAudioSamples != 144000 {
		return fmt.Errorf("program audio has %d frames, want 144000", program.TotalAudioSamples)
	}

	response, bundle, err := ctx.requestJSON(http.MethodGet, "/v1/conform-programs/program-joined-01/bundle", nil)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("program bundle returned %d: %s", response.StatusCode, bundle)
	}
	entries, err := readZip(bundle)
	if err != nil {
		return err
	}
	for _, name := range []string{
		"audio-program.wav",
		"video-program.y4m",
		"conform-program-receipt.json",
	} {
		if len(entries[name]) == 0 {
			return fmt.Errorf("program bundle entry %s is missing", name)
		}
	}
	var receipt domain.ConformProgramReceipt
	if err := json.Unmarshal(entries["conform-program-receipt.json"], &receipt); err != nil {
		return fmt.Errorf("decode program receipt: %w", err)
	}
	if len(receipt.Segments) != len(takes) {
		return fmt.Errorf("receipt has %d segments, want %d", len(receipt.Segments), len(takes))
	}

	audioClip, err := media.InspectAudio(entries["audio-program.wav"])
	if err != nil {
		return err
	}
	videoClip, err := media.InspectVideo(entries["video-program.y4m"])
	if err != nil {
		return err
	}
	if len(videoClip.Frames) != program.TotalFrames {
		return fmt.Errorf("joined picture has %d frames, want %d", len(videoClip.Frames), program.TotalFrames)
	}
	if audioClip.Info.FrameCount != program.TotalAudioSamples {
		return fmt.Errorf("joined audio has %d frames, want %d", audioClip.Info.FrameCount, program.TotalAudioSamples)
	}

	for index, take := range takes {
		segment := receipt.Segments[index]
		if segment.TakeID != take.takeID || segment.AnalysisID != take.analysisID {
			return fmt.Errorf("segment %d does not trace back to its source material", index)
		}
		if segment.SourceStartFrame != 0 || segment.SourceEndFrame != programFrames {
			return fmt.Errorf("segment %d source interval is [%d,%d)", index, segment.SourceStartFrame, segment.SourceEndFrame)
		}
		if segment.MeasuredOffsetMS != take.offsetMS || segment.AppliedOffsetMS != take.offsetMS {
			return fmt.Errorf("segment %d lost its own sync basis: %d ms", index, segment.MeasuredOffsetMS)
		}
		wantStart := index * programFrames
		if segment.OutputStartFrame != wantStart || segment.OutputFrameCount != programFrames {
			return fmt.Errorf("segment %d output placement is %d+%d", index, segment.OutputStartFrame, segment.OutputFrameCount)
		}
		wantAudioStart := index * 48000
		if segment.AudioStartSample != wantAudioStart || segment.AudioSampleFrames != 48000 {
			return fmt.Errorf("segment %d audio window is %d+%d", index, segment.AudioStartSample, segment.AudioSampleFrames)
		}
		if segment.InputTakeDigest == "" || segment.SegmentAudioDigest == "" {
			return fmt.Errorf("segment %d receipt is missing source digests", index)
		}
		if err := verifySegmentAligned(segment, audioClip, videoClip); err != nil {
			return fmt.Errorf("segment %d: %w", index, err)
		}
	}

	// An identical retry must be idempotent and return the same completed record.
	response, body, err = ctx.requestJSON(http.MethodPost, "/v1/conform-programs", map[string]any{
		"program_id":        "program-joined-01",
		"max_correction_ms": 500,
		"segments":          segments,
	})
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("program retry returned %d: %s", response.StatusCode, body)
	}
	var retry domain.ConformProgram
	if err := json.Unmarshal(body, &retry); err != nil {
		return err
	}
	if retry.BundleDigest != program.BundleDigest {
		return fmt.Errorf("program retry produced a different bundle digest")
	}
	return nil
}

func verifySegmentAligned(
	segment domain.ProgramSegmentReceipt,
	audio media.AudioClip,
	video media.VideoClip,
) error {
	channels := audio.Info.Channels
	audioStart := segment.AudioStartSample * channels
	audioEnd := (segment.AudioStartSample + segment.AudioSampleFrames) * channels
	segmentSamples := append(
		make([]int16, 0, audioEnd-audioStart),
		audio.Samples[audioStart:audioEnd]...,
	)
	segmentWAV, err := media.EncodePCM16(audio.Info.SampleRate, channels, segmentSamples)
	if err != nil {
		return err
	}
	segmentFrames := video.Frames[segment.OutputStartFrame : segment.OutputStartFrame+segment.OutputFrameCount]
	segmentVideo, err := media.EncodeY4M(video.Info, segmentFrames)
	if err != nil {
		return err
	}
	measurement, err := media.AnalyzeSync(segmentWAV, segmentVideo)
	if err != nil {
		return fmt.Errorf("re-analyze segment: %w", err)
	}
	if measurement.OffsetMS < -2 || measurement.OffsetMS > 2 {
		return fmt.Errorf("aligned segment still shows %d ms offset", measurement.OffsetMS)
	}
	return nil
}

func verifySplitDeterminism(ctx *checkContext) error {
	audio, video, err := media.CalibrationTake(media.CalibrationSpec{
		SampleRate:      programSampleRate,
		Channels:        1,
		Frames:          programFrames,
		FPS:             programFPS,
		Width:           programWidth,
		Height:          programHeight,
		AudioEventMS:    500,
		VideoEventFrame: 9,
		PeakAmplitude:   12000,
	})
	if err != nil {
		return err
	}
	if err := admitCustom(ctx, "program-take-d", audio, video); err != nil {
		return err
	}
	if err := analyzeCustom(ctx, "program-take-d", "program-analysis-d1", 125); err != nil {
		return err
	}
	if err := analyzeCustom(ctx, "program-take-d", "program-analysis-d2", 125); err != nil {
		return err
	}

	wholeBody, err := createProgramRecord(ctx, "program-whole-01", 500, []map[string]any{
		{"analysis_id": "program-analysis-d1"},
	})
	if err != nil {
		return err
	}
	splitBody, err := createProgramRecord(ctx, "program-split-01", 500, []map[string]any{
		{"analysis_id": "program-analysis-d1", "range": map[string]any{"start_frame": 0, "end_frame": 12}},
		{"analysis_id": "program-analysis-d2", "range": map[string]any{"start_frame": 12, "end_frame": 24}},
	})
	if err != nil {
		return err
	}
	var whole, split domain.ConformProgram
	if err := json.Unmarshal(wholeBody, &whole); err != nil {
		return err
	}
	if err := json.Unmarshal(splitBody, &split); err != nil {
		return err
	}
	if whole.AudioOutputDigest != split.AudioOutputDigest {
		return fmt.Errorf("splitting at a frame boundary changed the audio bytes (digest mismatch)")
	}
	if whole.VideoOutputDigest != split.VideoOutputDigest {
		return fmt.Errorf("splitting at a frame boundary changed the picture bytes (digest mismatch)")
	}
	if split.TotalAudioSamples != 48000 || split.TotalFrames != 24 {
		return fmt.Errorf("split program length drifted to %d audio frames / %d picture frames", split.TotalAudioSamples, split.TotalFrames)
	}
	if split.Segments[1].AudioStartSample != 24000 || split.Segments[1].SourceStartFrame != 12 {
		return fmt.Errorf("split junction did not land on the shared timeline: %+v", split.Segments[1])
	}
	return nil
}

func verifyProgramFailures(ctx *checkContext) error {
	// A correction above the ceiling becomes a terminal failed program with no
	// readable bundle, never a partial success package.
	response, body, err := ctx.requestJSON(http.MethodPost, "/v1/conform-programs", map[string]any{
		"program_id":        "program-ceiling-01",
		"max_correction_ms": 50,
		"segments":          []map[string]any{{"analysis_id": "program-analysis-a"}},
	})
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusUnprocessableEntity || errorCode(body) != "correction_limit_exceeded" {
		return fmt.Errorf("ceiling case returned %d: %s", response.StatusCode, body)
	}
	response, body, err = ctx.requestJSON(http.MethodGet, "/v1/conform-programs/program-ceiling-01", nil)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("failed program record returned %d: %s", response.StatusCode, body)
	}
	var failed domain.ConformProgram
	if err := json.Unmarshal(body, &failed); err != nil {
		return err
	}
	if failed.State != domain.ConformStateFailed || failed.BundleDigest != "" {
		return fmt.Errorf("failed program is not a clean terminal failure")
	}
	response, body, err = ctx.requestJSON(http.MethodGet, "/v1/conform-programs/program-ceiling-01/bundle", nil)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusConflict {
		return fmt.Errorf("failed program exposed a bundle: %d %s", response.StatusCode, body)
	}
	persisted, err := storage.New(ctx.root)
	if err != nil {
		return err
	}
	if _, _, err := persisted.ReadProgramBundle("program-ceiling-01"); err == nil {
		return fmt.Errorf("failed program bundle is readable on disk")
	}

	// An interval outside the source take is rejected before any record exists.
	response, body, err = ctx.requestJSON(http.MethodPost, "/v1/conform-programs", map[string]any{
		"program_id":        "program-range-01",
		"max_correction_ms": 500,
		"segments": []map[string]any{
			{"analysis_id": "program-analysis-a", "range": map[string]any{"start_frame": 20, "end_frame": 40}},
		},
	})
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusBadRequest || errorCode(body) != "invalid_segment_range" {
		return fmt.Errorf("bad range returned %d: %s", response.StatusCode, body)
	}
	if err := expectProgramMissing(ctx, "program-range-01"); err != nil {
		return err
	}

	// A missing sync basis leaves no program behind.
	response, body, err = ctx.requestJSON(http.MethodPost, "/v1/conform-programs", map[string]any{
		"program_id":        "program-missing-01",
		"max_correction_ms": 500,
		"segments":          []map[string]any{{"analysis_id": "does-not-exist"}},
	})
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusNotFound {
		return fmt.Errorf("missing analysis returned %d: %s", response.StatusCode, body)
	}
	if err := expectProgramMissing(ctx, "program-missing-01"); err != nil {
		return err
	}

	// Reusing an analysis twice in one program is rejected: each segment needs
	// its own sync basis.
	response, body, err = ctx.requestJSON(http.MethodPost, "/v1/conform-programs", map[string]any{
		"program_id":        "program-dupe-01",
		"max_correction_ms": 500,
		"segments": []map[string]any{
			{"analysis_id": "program-analysis-a"},
			{"analysis_id": "program-analysis-a"},
		},
	})
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusBadRequest {
		return fmt.Errorf("duplicate analysis returned %d: %s", response.StatusCode, body)
	}
	return nil
}

func expectProgramMissing(ctx *checkContext, id string) error {
	response, body, err := ctx.requestJSON(http.MethodGet, "/v1/conform-programs/"+id, nil)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusNotFound {
		return fmt.Errorf("program %s unexpectedly visible: %d %s", id, response.StatusCode, body)
	}
	response, _, err = ctx.requestJSON(http.MethodGet, "/v1/conform-programs/"+id+"/bundle", nil)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusNotFound {
		return fmt.Errorf("program %s bundle unexpectedly visible: %d", id, response.StatusCode)
	}
	return nil
}

func createProgramRecord(
	ctx *checkContext,
	id string,
	maxCorrection int,
	segments []map[string]any,
) ([]byte, error) {
	response, body, err := ctx.requestJSON(http.MethodPost, "/v1/conform-programs", map[string]any{
		"program_id":        id,
		"max_correction_ms": maxCorrection,
		"segments":          segments,
	})
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("program %s returned %d: %s", id, response.StatusCode, body)
	}
	return body, nil
}

func admitCustom(ctx *checkContext, id string, audio, video []byte) error {
	response, body, err := ctx.requestJSON(
		http.MethodPost,
		"/v1/takes",
		takePayload(id, "program take", audio, video),
	)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusCreated {
		return fmt.Errorf("admit %s returned %d: %s", id, response.StatusCode, body)
	}
	return nil
}

func analyzeCustom(ctx *checkContext, takeID, analysisID string, wantOffset int) error {
	response, body, err := ctx.requestJSON(
		http.MethodPost,
		"/v1/takes/"+takeID+"/sync-analyses",
		map[string]any{
			"analysis_id":    analysisID,
			"min_confidence": 0.55,
			"max_offset_ms":  1000,
		},
	)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusCreated {
		return fmt.Errorf("analysis %s returned %d: %s", analysisID, response.StatusCode, body)
	}
	var analysis domain.SyncAnalysis
	if err := json.Unmarshal(body, &analysis); err != nil {
		return err
	}
	if analysis.State != domain.AnalysisStateReady || analysis.OffsetMS == nil {
		return fmt.Errorf("analysis %s is not ready: %s", analysisID, analysis.ErrorMessage)
	}
	if *analysis.OffsetMS != wantOffset {
		return fmt.Errorf("analysis %s offset is %d, want %d", analysisID, *analysis.OffsetMS, wantOffset)
	}
	return nil
}
