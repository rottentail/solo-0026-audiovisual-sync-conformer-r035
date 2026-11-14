package domain

import "time"

// ProgramSegmentRange is the optional half-open picture interval taken from a
// source material. A nil pointer means "use the whole take".
type ProgramSegmentRange struct {
	StartFrame *int `json:"start_frame,omitempty"`
	EndFrame   *int `json:"end_frame,omitempty"`
}

// ProgramSegmentInput is one ordered segment of a multi-segment conform
// program. Every segment keeps its own sync basis through its own ready
// analysis.
type ProgramSegmentInput struct {
	AnalysisID string              `json:"analysis_id"`
	Range      ProgramSegmentRange `json:"range"`
}

// ProgramSegment is the resolved, persisted description of one rendered
// segment, including the exact source material and picture interval it came
// from and where it lands on the continuous output timeline.
type ProgramSegment struct {
	Index              int    `json:"index"`
	AnalysisID         string `json:"analysis_id"`
	TakeID             string `json:"take_id"`
	SourceStartFrame   int    `json:"source_start_frame"`
	SourceEndFrame     int    `json:"source_end_frame"`
	SourceFrameCount   int    `json:"source_frame_count"`
	MeasuredOffsetMS   int    `json:"measured_offset_ms"`
	AppliedOffsetMS    int    `json:"applied_offset_ms"`
	AppliedSamples     int    `json:"applied_samples"`
	OutputStartFrame   int    `json:"output_start_frame"`
	OutputFrameCount   int    `json:"output_frame_count"`
	AudioStartSample   int    `json:"audio_start_sample"`
	AudioSampleFrames  int    `json:"audio_sample_frames"`
	InputTakeDigest    string `json:"input_take_digest"`
	InputAudioDigest   string `json:"input_audio_digest"`
	InputVideoDigest   string `json:"input_video_digest"`
	SegmentAudioDigest string `json:"segment_audio_digest"`
}

// ConformProgram renders several received materials, in review order, into one
// continuous picture stream and one continuous audio track. Each segment is
// corrected with its own analysis while segment boundaries are computed on one
// shared sample timeline.
type ConformProgram struct {
	ID                string                `json:"id"`
	State             string                `json:"state"`
	MaxCorrectionMS   int                   `json:"max_correction_ms"`
	Segments          []ProgramSegment      `json:"segments"`
	SegmentRequests   []ProgramSegmentInput `json:"segment_requests"`
	TotalFrames       int                   `json:"total_frames,omitempty"`
	TotalAudioSamples int                   `json:"total_audio_samples,omitempty"`
	AudioOutputDigest string                `json:"audio_output_digest,omitempty"`
	VideoOutputDigest string                `json:"video_output_digest,omitempty"`
	BundleDigest      string                `json:"bundle_digest,omitempty"`
	BundleBytes       int                   `json:"bundle_bytes,omitempty"`
	ErrorCode         string                `json:"error_code,omitempty"`
	ErrorMessage      string                `json:"error_message,omitempty"`
	CreatedAt         time.Time             `json:"created_at"`
	UpdatedAt         time.Time             `json:"updated_at"`
}

// ProgramSegmentReceipt is the auditable per-segment entry inside a program
// receipt. It locates the exact source take and picture interval.
type ProgramSegmentReceipt struct {
	Index              int    `json:"index"`
	AnalysisID         string `json:"analysis_id"`
	TakeID             string `json:"take_id"`
	SourceStartFrame   int    `json:"source_start_frame"`
	SourceEndFrame     int    `json:"source_end_frame"`
	SourceFrameCount   int    `json:"source_frame_count"`
	MeasuredOffsetMS   int    `json:"measured_offset_ms"`
	AppliedOffsetMS    int    `json:"applied_offset_ms"`
	AppliedSamples     int    `json:"applied_samples"`
	OutputStartFrame   int    `json:"output_start_frame"`
	OutputFrameCount   int    `json:"output_frame_count"`
	AudioStartSample   int    `json:"audio_start_sample"`
	AudioSampleFrames  int    `json:"audio_sample_frames"`
	InputTakeDigest    string `json:"input_take_digest"`
	InputAudioDigest   string `json:"input_audio_digest"`
	InputVideoDigest   string `json:"input_video_digest"`
	SegmentAudioDigest string `json:"segment_audio_digest"`
}

// ConformProgramReceipt is the deterministic receipt stored inside a program
// review bundle.
type ConformProgramReceipt struct {
	SchemaVersion    int                     `json:"schema_version"`
	ProgramID        string                  `json:"program_id"`
	MaxCorrectionMS  int                     `json:"max_correction_ms"`
	Segments         []ProgramSegmentReceipt `json:"segments"`
	TotalFrames      int                     `json:"total_frames"`
	TotalAudioFrames int                     `json:"total_audio_frames"`
	SampleRate       int                     `json:"sample_rate"`
	Channels         int                     `json:"channels"`
	Width            int                     `json:"width"`
	Height           int                     `json:"height"`
	FPSNum           int                     `json:"fps_num"`
	FPSDen           int                     `json:"fps_den"`
	AudioDigest      string                  `json:"audio_digest"`
	VideoDigest      string                  `json:"video_digest"`
	CreatedAt        time.Time               `json:"created_at"`
}

type CreateProgramInput struct {
	ProgramID       string
	MaxCorrectionMS int
	Segments        []ProgramSegmentInput
}

type ProgramResult struct {
	Program ConformProgram
	Created bool
}
