package workagent

import (
	"fmt"

	"server/globals"
	workagentModel "server/model/workagent"
)

// updateThreadFiles registers thread files via the shared FileService.
//
// This is the only surviving entry point from the former
// content_processing.go helpers; the rest of that file — an unused
// "intelligent preview" pipeline that read arbitrary filesystem paths
// (readFileContent → readTextFile/readJSONFile) without validating them
// against the workspace root — has been deleted. If you need preview or
// chunking in the future, route it through ResolveInsideWorkspace so a
// malicious FilePath from the DB can't pull /etc/passwd into a prompt.
func (api *AIChatApiNew) updateThreadFiles(chatThread *workagentModel.ChatThread, files []FileInfo, source string) error {
	if chatThread == nil || len(files) == 0 {
		return nil
	}

	globals.Info(fmt.Sprintf("Updating thread %d with %d files from source: %s", chatThread.Id, len(files), source))

	for _, file := range files {
		threadFileRequest := workagentModel.ThreadFileRequest{
			UID:      chatThread.UID,
			ThreadID: chatThread.Id,
			FileName: file.Name,
			FileSize: uint64(file.Size),
			FileType: file.Type,
			FilePath: file.FilePath,
		}

		if _, err := api.fileService.AddFile(threadFileRequest); err != nil {
			globals.Warn(fmt.Sprintf("Failed to add file %s to thread %d: %v", file.Name, chatThread.Id, err))
			// Continue with other files even if one fails
		}
	}

	return nil
}
