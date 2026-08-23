package apigen

// Is matches ApiErr values on status code and internal code, ignoring the
// display message. Sentinel errors like reference_in_use attach per-instance
// display detail (naming the referencing deployments), which would otherwise
// break errors.Is against the bare sentinel value.
func (e ApiErr) Is(target error) bool {
	t, ok := target.(ApiErr)
	if !ok {
		return false
	}
	return e.Code == t.Code && e.InternalErr == t.InternalErr
}
