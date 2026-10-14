package httpapi

import (
	"errors"

	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
)

// parameterDetail describes a binding error from the generated wrapper without
// echoing the offending value or any parser internals.
func parameterDetail(err error) string {
	var (
		format   *oapi.InvalidParamFormatError
		required *oapi.RequiredParamError
		header   *oapi.RequiredHeaderError
		many     *oapi.TooManyValuesForParamError
	)
	switch {
	case errors.As(err, &format):
		return `"` + format.ParamName + `" has an invalid format.`
	case errors.As(err, &required):
		return `"` + required.ParamName + `" is required.`
	case errors.As(err, &header):
		return `Header "` + header.ParamName + `" is required.`
	case errors.As(err, &many):
		return `"` + many.ParamName + `" must be given once.`
	default:
		return "A request parameter is invalid."
	}
}
