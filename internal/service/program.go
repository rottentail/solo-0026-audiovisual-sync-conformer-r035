package service

import (
	"fmt"
	"time"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/domain"
	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/media"
)

type resolvedProgramSegment struct {
	analysis domain.SyncAnalysis
	take     domain.MediaTake
	start    int
	end      int
}

// CreateProgram validates and renders one multi-segment conform program. Each
// segment keeps its own ready analysis as the sync basis; the media layer
// joins them on one cumulative sample timeline.
func (s *Service) CreateProgram(input domain.CreateProgramInput) (domain.ProgramResult, error) {
	if err := domain.ValidateCreateProgram(input); err != nil {
		return domain.ProgramResult{}, err
	}
	resolved := make([]resolvedProgramSegment, 0, len(input.Segments))
	segments := make([]domain.ProgramSegment, 0, len(input.Segments))
	for index, request := range input.Segments {
		analysis, err := s.repository.GetAnalysis(request.AnalysisID)
		if err != nil {
			return domain.ProgramResult{}, err
		}
		if analysis.State != domain.AnalysisStateReady || analysis.OffsetMS == nil {
			return domain.ProgramResult{}, domain.NewError(
				"analysis_not_ready",
				409,
				"segments[%d] analysis %s is not ready for a conform",
				index,
				request.AnalysisID,
			)
		}
		take, err := s.repository.GetTake(analysis.TakeID)
		if err != nil {
			return domain.ProgramResult{}, err
		}
		start, end, err := resolveSegmentRange(request.Range, take.Video.FrameCount)
		if err != nil {
			return domain.ProgramResult{}, domain.NewError(
				"invalid_segment_range",
				400,
				"segments[%d]: %s",
				index,
				err.Error(),
			)
		}
		resolved = append(resolved, resolvedProgramSegment{
			analysis: analysis,
			take:     take,
			start:    start,
			end:      end,
		})
		segments = append(segments, domain.ProgramSegment{
			Index:            index,
			AnalysisID:       analysis.ID,
			TakeID:           take.ID,
			SourceStartFrame: start,
			SourceEndFrame:   end,
			SourceFrameCount: end - start,
			MeasuredOffsetMS: *analysis.OffsetMS,
			AppliedOffsetMS:  *analysis.OffsetMS,
			InputTakeDigest:  take.Digest,
			InputAudioDigest: take.Audio.Digest,
			InputVideoDigest: take.Video.Digest,
		})
	}

	now := time.Now().UTC()
	record := domain.ConformProgram{
		ID:              input.ProgramID,
		State:           domain.ConformStateQueued,
		MaxCorrectionMS: input.MaxCorrectionMS,
		Segments:        segments,
		SegmentRequests: append([]domain.ProgramSegmentInput(nil), input.Segments...),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	existing, created, err := s.repository.CreateProgram(record)
	if err != nil {
		return domain.ProgramResult{}, err
	}
	if !created {
		return domain.ProgramResult{Program: existing, Created: false}, nil
	}
	for index, segment := range resolved {
		if abs(*segment.analysis.OffsetMS) > record.MaxCorrectionMS {
			return s.failProgram(record, "correction_limit_exceeded", fmt.Sprintf(
				"segments[%d] measured correction of %d ms exceeds the ceiling of %d ms",
				index,
				abs(*segment.analysis.OffsetMS),
				record.MaxCorrectionMS,
			))
		}
	}

	record.State = domain.ConformStateRendering
	record.UpdatedAt = time.Now().UTC()
	if err := s.repository.UpdateProgram(record); err != nil {
		return domain.ProgramResult{}, err
	}
	return s.renderProgram(record, resolved)
}

func (s *Service) renderProgram(
	record domain.ConformProgram,
	resolved []resolvedProgramSegment,
) (domain.ProgramResult, error) {
	requests := make([]media.ProgramSegmentRequest, 0, len(resolved))
	for index, segment := range resolved {
		audio, video, err := s.repository.ReadTakeMedia(segment.take.ID)
		if err != nil {
			return s.failProgram(record, "take_media_unavailable",
				fmt.Sprintf("segments[%d]: %s", index, err.Error()))
		}
		requests = append(requests, media.ProgramSegmentRequest{
			Audio:            audio,
			Video:            video,
			MeasuredOffsetMS: *segment.analysis.OffsetMS,
			StartFrame:       segment.start,
			EndFrame:         segment.end,
		})
	}
	build, err := media.BuildProgram(requests, record.MaxCorrectionMS)
	if err != nil {
		stable := domain.AsError(err)
		if stable.Code == "internal_error" {
			return s.failProgram(record, "program_render_failed", err.Error())
		}
		return s.failProgram(record, stable.Code, stable.Message)
	}

	for index := range record.Segments {
		output := build.Segments[index]
		segment := &record.Segments[index]
		segment.AppliedSamples = output.AppliedSamples
		segment.OutputStartFrame = output.OutputStartFrame
		segment.OutputFrameCount = output.OutputFrameCount
		segment.AudioStartSample = output.AudioStartSample
		segment.AudioSampleFrames = output.AudioSampleFrames
		segment.SegmentAudioDigest = output.SegmentAudioDigest
	}
	record.TotalFrames = build.TotalFrames
	record.TotalAudioSamples = build.TotalAudioFrames
	record.AudioOutputDigest = build.AudioDigest
	record.VideoOutputDigest = build.VideoDigest

	receiptSegments := make([]domain.ProgramSegmentReceipt, 0, len(record.Segments))
	for _, segment := range record.Segments {
		receiptSegments = append(receiptSegments, domain.ProgramSegmentReceipt{
			Index:              segment.Index,
			AnalysisID:         segment.AnalysisID,
			TakeID:             segment.TakeID,
			SourceStartFrame:   segment.SourceStartFrame,
			SourceEndFrame:     segment.SourceEndFrame,
			SourceFrameCount:   segment.SourceFrameCount,
			MeasuredOffsetMS:   segment.MeasuredOffsetMS,
			AppliedOffsetMS:    segment.AppliedOffsetMS,
			AppliedSamples:     segment.AppliedSamples,
			OutputStartFrame:   segment.OutputStartFrame,
			OutputFrameCount:   segment.OutputFrameCount,
			AudioStartSample:   segment.AudioStartSample,
			AudioSampleFrames:  segment.AudioSampleFrames,
			InputTakeDigest:    segment.InputTakeDigest,
			InputAudioDigest:   segment.InputAudioDigest,
			InputVideoDigest:   segment.InputVideoDigest,
			SegmentAudioDigest: segment.SegmentAudioDigest,
		})
	}
	receipt := domain.ConformProgramReceipt{
		SchemaVersion:    1,
		ProgramID:        record.ID,
		MaxCorrectionMS:  record.MaxCorrectionMS,
		Segments:         receiptSegments,
		TotalFrames:      build.TotalFrames,
		TotalAudioFrames: build.TotalAudioFrames,
		SampleRate:       build.AudioInfo.SampleRate,
		Channels:         build.AudioInfo.Channels,
		Width:            build.VideoInfo.Width,
		Height:           build.VideoInfo.Height,
		FPSNum:           build.VideoInfo.FPSNum,
		FPSDen:           build.VideoInfo.FPSDen,
		AudioDigest:      build.AudioDigest,
		VideoDigest:      build.VideoDigest,
		CreatedAt:        record.CreatedAt,
	}
	bundle, receiptData, err := media.BuildProgramBundle(build.WAV, build.Video, receipt)
	if err != nil {
		return s.failProgram(record, "bundle_build_failed", err.Error())
	}
	record.BundleDigest = media.DigestBytes(bundle)
	record.BundleBytes = len(bundle)
	record.State = domain.ConformStateCommitting
	record.UpdatedAt = time.Now().UTC()
	if err := s.repository.UpdateProgram(record); err != nil {
		return domain.ProgramResult{}, err
	}
	completed, err := s.repository.CommitProgram(record, bundle, receiptData)
	if err != nil {
		return s.failProgram(record, "bundle_commit_failed", err.Error())
	}
	return domain.ProgramResult{Program: completed, Created: true}, nil
}

func (s *Service) failProgram(
	record domain.ConformProgram,
	code string,
	message string,
) (domain.ProgramResult, error) {
	record.State = domain.ConformStateFailed
	record.ErrorCode = code
	record.ErrorMessage = message
	record.UpdatedAt = time.Now().UTC()
	if err := s.repository.UpdateProgram(record); err != nil {
		return domain.ProgramResult{}, err
	}
	return domain.ProgramResult{Program: record, Created: true}, domain.NewError(code, 422, message)
}

func (s *Service) GetProgram(id string) (domain.ConformProgram, error) {
	if err := domain.ValidateIdentifier("program_id", id); err != nil {
		return domain.ConformProgram{}, err
	}
	return s.repository.GetProgram(id)
}

func (s *Service) ReadProgramBundle(id string) ([]byte, domain.ConformProgramReceipt, error) {
	if err := domain.ValidateIdentifier("program_id", id); err != nil {
		return nil, domain.ConformProgramReceipt{}, err
	}
	return s.repository.ReadProgramBundle(id)
}

func resolveSegmentRange(r domain.ProgramSegmentRange, frameCount int) (int, int, error) {
	start := 0
	if r.StartFrame != nil {
		start = *r.StartFrame
	}
	end := frameCount
	if r.EndFrame != nil {
		end = *r.EndFrame
	}
	if start < 0 || end <= start || end > frameCount {
		return 0, 0, fmt.Errorf(
			"picture interval [%d,%d) is outside the source take of %d frames",
			start,
			end,
			frameCount,
		)
	}
	return start, end, nil
}
