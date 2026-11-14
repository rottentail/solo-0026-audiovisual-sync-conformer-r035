package domain

const (
	// MaxProgramSegments bounds how many received materials one program can
	// join so a single request stays deterministic and reviewable.
	MaxProgramSegments = 64
	// MaxProgramFrames bounds the total picture length of a joined program.
	MaxProgramFrames = 4800
)

// ValidateCreateProgram checks the transport-level shape of a multi-segment
// program request. Cross-entity checks (ready analyses, geometry, ranges)
// happen in the service layer against persisted records.
func ValidateCreateProgram(input CreateProgramInput) error {
	if err := ValidateIdentifier("program_id", input.ProgramID); err != nil {
		return err
	}
	if input.MaxCorrectionMS < 1 || input.MaxCorrectionMS > MaxCorrectionMS {
		return NewError(
			"invalid_request",
			400,
			"max_correction_ms must be between 1 and %d",
			MaxCorrectionMS,
		)
	}
	if len(input.Segments) == 0 {
		return NewError("invalid_request", 400, "segments must contain at least one entry")
	}
	if len(input.Segments) > MaxProgramSegments {
		return NewError(
			"invalid_request",
			400,
			"a program can contain at most %d segments",
			MaxProgramSegments,
		)
	}
	seen := make(map[string]bool, len(input.Segments))
	for index, segment := range input.Segments {
		if err := ValidateIdentifier("analysis_id", segment.AnalysisID); err != nil {
			return NewError(
				"invalid_identifier",
				400,
				"segments[%d].analysis_id: %s",
				index,
				err.Error(),
			)
		}
		if seen[segment.AnalysisID] {
			return NewError(
				"invalid_request",
				400,
				"segments[%d] reuses analysis %s; every segment needs its own sync basis",
				index,
				segment.AnalysisID,
			)
		}
		seen[segment.AnalysisID] = true
		if err := validateSegmentRange(segment.Range); err != nil {
			return NewError(
				errCode(err),
				errStatus(err),
				"segments[%d].range: %s",
				index,
				errMessage(err),
			)
		}
	}
	return nil
}

func validateSegmentRange(r ProgramSegmentRange) error {
	start := 0
	if r.StartFrame != nil {
		start = *r.StartFrame
	}
	if start < 0 {
		return NewError("invalid_request", 400, "start_frame must not be negative")
	}
	if r.EndFrame != nil {
		if *r.EndFrame <= start {
			return NewError(
				"invalid_request",
				400,
				"end_frame must be greater than start_frame",
			)
		}
	}
	return nil
}

func errCode(err error) string    { return AsError(err).Code }
func errStatus(err error) int     { return AsError(err).Status }
func errMessage(err error) string { return AsError(err).Message }
