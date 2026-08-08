package tools

import (
	"errors"
	"strconv"
	"strings"

	"server/globals"
	projectService "server/service/project"
	canvasService "server/service/tools/canvas"
)

func parseAndAuthorizeCanvasThreadProjectID(raw string, uid uint) (uint, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, nil
	}
	id, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil || id == 0 {
		return 0, canvasService.ErrCanvasProjectNotFound
	}
	exists, err := canvasProjectExistsForUser(globals.GraDBs["system"], id, int(uid))
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, canvasService.ErrCanvasProjectNotFound
	}
	return uint(id), nil
}

func parseAndAuthorizeCanvasThreadProjectIDForEdit(raw string, uid uint) (uint, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, nil
	}
	id, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil || id == 0 {
		return 0, canvasService.ErrCanvasProjectNotFound
	}
	canEdit, err := projectService.NewRepository(globals.GraDBs["system"]).CanEditProject(uint(id), uid)
	if err != nil {
		return 0, err
	}
	if !canEdit {
		return 0, canvasService.ErrCanvasProjectNotFound
	}
	return uint(id), nil
}

func ensureCanvasThreadProjectScope(existingProjectID uint, requestedProjectID uint) (uint, error) {
	if requestedProjectID == 0 {
		return existingProjectID, nil
	}
	if existingProjectID != 0 && existingProjectID != requestedProjectID {
		return 0, errors.New("canvas thread belongs to a different project")
	}
	return requestedProjectID, nil
}
