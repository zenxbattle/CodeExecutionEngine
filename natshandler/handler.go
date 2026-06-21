package natshandler

import (
	"encoding/json"
	"log"

	"github.com/nats-io/nats.go"
	"zenxbattle/model"
	"zenxbattle/service"
	"zenxbattle/worker"
)

func StartHandlers(nc *nats.Conn, pool worker.WorkerPool, showOutput bool) {
	svc := service.NewCompilerService(pool, showOutput)

	nc.Subscribe("compiler.execute.request", func(msg *nats.Msg) {
		var req model.CompilerRequest
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			log.Printf("Failed to parse: %v", err)
			return
		}
		res := svc.Execute(req.Code, req.Language)
		resData, _ := json.Marshal(res)
		msg.Respond(resData)
	})

	nc.Subscribe("problems.execute.request", func(msg *nats.Msg) {
		var req model.CompilerRequest
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			log.Printf("Failed to parse: %v", err)
			return
		}
		res := svc.Execute(req.Code, req.Language)
		resData, _ := json.Marshal(res)
		msg.Respond(resData)
	})

	log.Println("NATS handlers started: compiler.execute.request, problems.execute.request")
}
