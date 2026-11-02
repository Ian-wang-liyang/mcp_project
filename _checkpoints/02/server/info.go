package server

import (
	"bufio"
	"context"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type workspaceInfo struct {
	Root      string `json:"root" jsonschema:"absolute path of the workspace root"`
	Module    string `json:"module" jsonschema:"module path from go.mod, empty if there is no go.mod"`
	GoVersion string `json:"go_version" jsonschema:"go directive from go.mod, empty if absent"`
}

func addInfoTool(s *mcp.Server, ws *workspace) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "workspace_info",
		Description: "Describe the Go workspace: its root directory, module path and Go version.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, workspaceInfo, error) {
		info := workspaceInfo{Root: ws.root}
		f, err := os.OpenInRoot(ws.root, "go.mod")
		if os.IsNotExist(err) {
			return nil, info, nil
		}
		if err != nil {
			return nil, workspaceInfo{}, err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 2 {
				continue
			}
			switch fields[0] {
			case "module":
				info.Module = strings.Trim(fields[1], `"`)
			case "go":
				info.GoVersion = fields[1]
			}
		}
		return nil, info, sc.Err()
	})
}
