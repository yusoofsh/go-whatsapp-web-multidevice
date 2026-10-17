package rest

import (
	"errors"

	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

// clientErrorResponse writes a 4xx GenericError (such as a ValidationError
// returned by a usecase) as the standard JSON envelope. It reports false for
// any other error so callers keep their existing handling. Answering here
// avoids panicking through utils.PanicIfNeeded and the Recovery middleware,
// which logs every bad query string as "Panic recovered".
func clientErrorResponse(c fiber.Ctx, err error) (bool, error) {
	var genericErr pkgError.GenericError
	if !errors.As(err, &genericErr) {
		return false, nil
	}
	status := genericErr.StatusCode()
	if status < fiber.StatusBadRequest || status >= fiber.StatusInternalServerError {
		return false, nil
	}
	return true, c.Status(status).JSON(utils.ResponseData{
		Status:  status,
		Code:    genericErr.ErrCode(),
		Message: genericErr.Error(),
	})
}
