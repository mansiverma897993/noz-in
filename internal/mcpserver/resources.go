package mcpserver

import (
	"context"
	"strings"

	"github.com/mansiverma897993/signoz/internal/model"
	"github.com/mark3labs/mcp-go/mcp"
)

const reasonCodeURI = "promcast://reason-codes"

func (service *Service) registerResources() {
	resource := mcp.NewResource(
		reasonCodeURI,
		"SigNoz migration reason codes",
		mcp.WithResourceDescription("The closed compatibility taxonomy used by dashboard and rule reports."),
		mcp.WithMIMEType("text/markdown"),
	)
	service.server.AddResource(resource, func(_ context.Context, _ mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		var content strings.Builder
		content.WriteString("# promcast reason codes\n\n")
		for _, code := range model.ReasonCodes() {
			description, _ := model.ReasonDescription(code)
			content.WriteString("## ")
			content.WriteString(string(code))
			content.WriteString("\n\n")
			content.WriteString(description)
			content.WriteString("\n\n")
		}
		return []mcp.ResourceContents{mcp.TextResourceContents{
			URI: reasonCodeURI, MIMEType: "text/markdown", Text: content.String(),
		}}, nil
	})
}
