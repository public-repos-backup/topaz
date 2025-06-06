package access

import (
	"github.com/aserto-dev/topaz/pkg/cli/cc"
	"github.com/aserto-dev/topaz/pkg/cli/clients"
	dsc "github.com/aserto-dev/topaz/pkg/cli/clients/directory"
	"github.com/aserto-dev/topaz/pkg/cli/jsonx"
	dsa1 "github.com/authzen/access.go/api/access/v1"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

type EvaluationsCmd struct {
	clients.RequestArgs
	dsc.Config
	req  dsa1.EvaluationRequest
	resp dsa1.EvaluationResponse
}

func (cmd *EvaluationsCmd) Run(c *cc.CommonCtx) error {
	if cmd.Template {
		return jsonx.OutputJSONPB(c.StdOut(), cmd.template())
	}

	if err := cmd.Process(c, &cmd.req, cmd.template); err != nil {
		return err
	}

	if err := cmd.Invoke(c.Context, dsa1.Access_Evaluations_FullMethodName, &cmd.req, &cmd.resp); err != nil {
		return err
	}

	return jsonx.OutputJSONPB(c.StdOut(), &cmd.resp)
}

func (cmd *EvaluationsCmd) template() proto.Message {
	return &dsa1.EvaluationsRequest{
		Subject: &dsa1.Subject{
			Type:       "",
			Id:         "",
			Properties: &structpb.Struct{},
		},
		Action: &dsa1.Action{
			Name:       "",
			Properties: &structpb.Struct{},
		},
		Resource: &dsa1.Resource{
			Type:       "",
			Id:         "",
			Properties: &structpb.Struct{},
		},
		Context:     &structpb.Struct{},
		Evaluations: []*dsa1.EvaluationRequest{},
		Options:     &structpb.Struct{},
	}
}
