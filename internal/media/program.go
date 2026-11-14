package media

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/domain"
)

// ProgramSegmentRequest is one resolved segment handed to the media builder.
// StartFrame and EndFrame are a half-open picture interval already validated
// against the source take by the service layer.
type ProgramSegmentRequest struct {
	Audio            []byte
	Video            []byte
	MeasuredOffsetMS int
	StartFrame       int
	EndFrame         int
}

// ProgramSegmentOutput reports how one segment landed on the continuous
// output timeline. Values are expressed in picture frames and in audio sample
// frames (one frame = one sample per channel).
type ProgramSegmentOutput struct {
	Index              int
	MeasuredOffsetMS   int
	AppliedOffsetMS    int
	AppliedSamples     int
	SourceStartFrame   int
	SourceEndFrame     int
	SourceFrameCount   int
	OutputStartFrame   int
	OutputFrameCount   int
	AudioStartSample   int
	AudioSampleFrames  int
	SegmentAudioDigest string
	InputAudioDigest   string
	InputVideoDigest   string
}

// ProgramBuildResult is the fully rendered continuous program before it is
// packaged into a ZIP bundle.
type ProgramBuildResult struct {
	WAV              []byte
	Video            []byte
	Segments         []ProgramSegmentOutput
	TotalFrames      int
	TotalAudioFrames int
	VideoInfo        domain.VideoInfo
	AudioInfo        domain.AudioInfo
	AudioDigest      string
	VideoDigest      string
}

type resolvedSegment struct {
	audio    AudioClip
	video    VideoClip
	offsetMS int
	start    int
	end      int
	shift    int
}

// BuildProgram joins several received materials into one continuous audio and
// picture stream. Every segment is corrected with its own measured offset,
// while all segment boundaries are derived from one cumulative sample
// timeline so independent per-segment rounding can never add or drop sound at
// a junction or accumulate drift as the segment count grows.
func BuildProgram(
	requests []ProgramSegmentRequest,
	maxCorrectionMS int,
) (ProgramBuildResult, error) {
	if len(requests) == 0 {
		return ProgramBuildResult{}, fmt.Errorf("program has no segments")
	}
	resolved := make([]resolvedSegment, 0, len(requests))
	var geometry struct {
		sampleRate int
		channels   int
		width      int
		height     int
		fpsNum     int
		fpsDen     int
		chroma     string
	}
	for index, request := range requests {
		if absInt(request.MeasuredOffsetMS) > maxCorrectionMS {
			return ProgramBuildResult{}, domain.NewError(
				"correction_limit_exceeded",
				422,
				"segment %d correction of %d ms exceeds the ceiling of %d ms",
				index,
				absInt(request.MeasuredOffsetMS),
				maxCorrectionMS,
			)
		}
		audio, err := InspectAudio(request.Audio)
		if err != nil {
			return ProgramBuildResult{}, fmt.Errorf("segment %d audio probe failed: %w", index, err)
		}
		video, err := InspectVideo(request.Video)
		if err != nil {
			return ProgramBuildResult{}, fmt.Errorf("segment %d picture probe failed: %w", index, err)
		}
		if index == 0 {
			geometry = struct {
				sampleRate int
				channels   int
				width      int
				height     int
				fpsNum     int
				fpsDen     int
				chroma     string
			}{
				sampleRate: audio.Info.SampleRate,
				channels:   audio.Info.Channels,
				width:      video.Info.Width,
				height:     video.Info.Height,
				fpsNum:     video.Info.FPSNum,
				fpsDen:     video.Info.FPSDen,
				chroma:     video.Info.Chroma,
			}
		} else if audio.Info.SampleRate != geometry.sampleRate ||
			audio.Info.Channels != geometry.channels ||
			video.Info.Width != geometry.width ||
			video.Info.Height != geometry.height ||
			video.Info.FPSNum != geometry.fpsNum ||
			video.Info.FPSDen != geometry.fpsDen ||
			video.Info.Chroma != geometry.chroma {
			return ProgramBuildResult{}, domain.NewError(
				"segment_geometry_mismatch",
				409,
				"segment %d geometry does not match the first segment",
				index,
			)
		}
		if request.StartFrame < 0 ||
			request.EndFrame <= request.StartFrame ||
			request.EndFrame > video.Info.FrameCount {
			return ProgramBuildResult{}, fmt.Errorf(
				"segment %d picture interval [%d,%d) is outside its %d frames",
				index,
				request.StartFrame,
				request.EndFrame,
				video.Info.FrameCount,
			)
		}
		shift := int(math.Round(
			float64(request.MeasuredOffsetMS) * float64(geometry.sampleRate) / 1000,
		))
		resolved = append(resolved, resolvedSegment{
			audio:    audio,
			video:    video,
			offsetMS: request.MeasuredOffsetMS,
			start:    request.StartFrame,
			end:      request.EndFrame,
			shift:    shift,
		})
	}

	// Cumulative picture frame counts drive a single audio sample timeline.
	frameCountAt := func(segmentEnd int) int {
		total := 0
		for i := 0; i < segmentEnd; i++ {
			total += resolved[i].end - resolved[i].start
		}
		return total
	}
	audioBoundary := func(cumulativeFrames int) int {
		return roundDiv(
			int64(cumulativeFrames)*int64(geometry.sampleRate)*int64(geometry.fpsDen),
			int64(geometry.fpsNum),
		)
	}
	sourceBoundary := func(segment resolvedSegment, frame int) int {
		return roundDiv(
			int64(frame)*int64(geometry.sampleRate)*int64(geometry.fpsDen),
			int64(geometry.fpsNum),
		)
	}

	outputs := make([]ProgramSegmentOutput, 0, len(resolved))
	var joinedSamples []int16
	joinedFrames := make([][]byte, 0)
	totalFrames := 0
	for index, segment := range resolved {
		clip := segment.audio
		// Align the whole source track by the inverse measured offset, using
		// the same sample-accurate rule as the single-take conform.
		aligned := make([]int16, len(clip.Samples))
		sampleDelta := segment.shift * geometry.channels
		for outputIndex := range aligned {
			sourceIndex := outputIndex + sampleDelta
			if sourceIndex >= 0 && sourceIndex < len(clip.Samples) {
				aligned[outputIndex] = clip.Samples[sourceIndex]
			}
		}

		windowStart := sourceBoundary(segment, segment.start)
		windowEnd := sourceBoundary(segment, segment.end)
		if windowStart > len(clip.Samples)/geometry.channels {
			windowStart = len(clip.Samples) / geometry.channels
		}
		if windowEnd > len(clip.Samples)/geometry.channels {
			windowEnd = len(clip.Samples) / geometry.channels
		}
		windowFrames := append(
			make([]int16, 0, (windowEnd-windowStart)*geometry.channels),
			aligned[windowStart*geometry.channels:windowEnd*geometry.channels]...,
		)

		outputStartFrame := totalFrames
		outputFrameCount := segment.end - segment.start
		audioStartSample := audioBoundary(frameCountAt(index))
		targetAudioFrames := audioBoundary(frameCountAt(index+1)) - audioStartSample

		// Stretch only the final tail (silence) to exactly fill the shared
		// timeline slot. Boundaries are computed cumulatively, so the total
		// length never drifts with the number of segments.
		switch {
		case len(windowFrames)/geometry.channels > targetAudioFrames:
			windowFrames = windowFrames[:targetAudioFrames*geometry.channels]
		case len(windowFrames)/geometry.channels < targetAudioFrames:
			shortfall := targetAudioFrames - len(windowFrames)/geometry.channels
			windowFrames = append(windowFrames, make([]int16, shortfall*geometry.channels)...)
		}
		segmentWAV, err := EncodePCM16(geometry.sampleRate, geometry.channels, windowFrames)
		if err != nil {
			return ProgramBuildResult{}, fmt.Errorf("segment %d audio encode failed: %w", index, err)
		}
		outputs = append(outputs, ProgramSegmentOutput{
			Index:              index,
			MeasuredOffsetMS:   segment.offsetMS,
			AppliedOffsetMS:    segment.offsetMS,
			AppliedSamples:     segment.shift,
			SourceStartFrame:   segment.start,
			SourceEndFrame:     segment.end,
			SourceFrameCount:   outputFrameCount,
			OutputStartFrame:   outputStartFrame,
			OutputFrameCount:   outputFrameCount,
			AudioStartSample:   audioStartSample,
			AudioSampleFrames:  targetAudioFrames,
			SegmentAudioDigest: DigestBytes(segmentWAV),
			InputAudioDigest:   clip.Info.Digest,
			InputVideoDigest:   segment.video.Info.Digest,
		})
		joinedSamples = append(joinedSamples, windowFrames...)
		joinedFrames = append(joinedFrames, segment.video.Frames[segment.start:segment.end]...)
		totalFrames += outputFrameCount
	}

	if totalFrames > domain.MaxProgramFrames {
		return ProgramBuildResult{}, domain.NewError(
			"program_too_long",
			400,
			"program spans %d frames, which exceeds the %d frame limit",
			totalFrames,
			domain.MaxProgramFrames,
		)
	}

	wav, err := EncodePCM16(geometry.sampleRate, geometry.channels, joinedSamples)
	if err != nil {
		return ProgramBuildResult{}, fmt.Errorf("encode program audio: %w", err)
	}
	videoInfo := domain.VideoInfo{
		Width:      geometry.width,
		Height:     geometry.height,
		FPSNum:     geometry.fpsNum,
		FPSDen:     geometry.fpsDen,
		FrameCount: totalFrames,
		Chroma:     geometry.chroma,
	}
	videoBytes, err := EncodeY4M(videoInfo, joinedFrames)
	if err != nil {
		return ProgramBuildResult{}, fmt.Errorf("encode program picture: %w", err)
	}
	videoInfo.DurationMS = totalFrames * 1000 * geometry.fpsDen / geometry.fpsNum
	videoInfo.Digest = DigestBytes(videoBytes)
	videoInfo.ByteCount = len(videoBytes)

	audioClip, err := InspectAudio(wav)
	if err != nil {
		return ProgramBuildResult{}, fmt.Errorf("probe program audio: %w", err)
	}
	return ProgramBuildResult{
		WAV:              wav,
		Video:            videoBytes,
		Segments:         outputs,
		TotalFrames:      totalFrames,
		TotalAudioFrames: len(joinedSamples) / geometry.channels,
		VideoInfo:        videoInfo,
		AudioInfo:        audioClip.Info,
		AudioDigest:      DigestBytes(wav),
		VideoDigest:      videoInfo.Digest,
	}, nil
}

// BuildProgramBundle writes the deterministic ZIP bundle for a joined program.
// The receipt is embedded so every output segment can be traced back to its
// source take and picture interval.
func BuildProgramBundle(
	audio []byte,
	video []byte,
	receipt domain.ConformProgramReceipt,
) ([]byte, []byte, error) {
	receiptData, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("encode program receipt: %w", err)
	}
	receiptData = append(receiptData, '\n')

	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	entries := []struct {
		name string
		data []byte
	}{
		{name: "audio-program.wav", data: audio},
		{name: "video-program.y4m", data: video},
		{name: "conform-program-receipt.json", data: receiptData},
	}
	for _, entry := range entries {
		header := &zip.FileHeader{
			Name:     entry.name,
			Method:   zip.Store,
			Modified: time.Unix(0, 0).UTC(),
		}
		header.SetMode(0o600)
		part, err := writer.CreateHeader(header)
		if err != nil {
			return nil, nil, fmt.Errorf("create ZIP entry: %w", err)
		}
		if _, err := part.Write(entry.data); err != nil {
			return nil, nil, fmt.Errorf("write ZIP entry: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, nil, fmt.Errorf("close ZIP bundle: %w", err)
	}
	return output.Bytes(), receiptData, nil
}

// roundDiv divides non-negative integers using round-half-up so cumulative
// boundary positions stay on one deterministic timeline.
func roundDiv(numerator, denominator int64) int {
	if denominator <= 0 {
		return 0
	}
	return int((numerator + denominator/2) / denominator)
}
