package natshandler

import (
	"encoding/json"
	"log"
	"zenxbattle/executor"
	"zenxbattle/service"

	"zenxbattle/model"
)

func HandleCompilerRequest(msg []byte, workerPool *executor.WorkerPool, showOutput bool) []byte {
	var req model.CompilerRequest
	if err := json.Unmarshal(msg, &req); err != nil {
		log.Printf("Failed to parse execution request: %v", err)
		return nil
	}

	compilerService := service.NewCompilerService(workerPool, showOutput)

	res, err := compilerService.Compile(req.Code, req.Language)
	if err != nil {
		log.Printf("Failed to compile code: %v", err)
		return nil
	}

	resData, _ := json.Marshal(res)
	return resData
}

func HandleProblemRunRequest(msg []byte, workerPool *executor.WorkerPool, showOutput bool) []byte {
	var req model.ProblemExecutionRequest
	if err := json.Unmarshal(msg, &req); err != nil {
		log.Printf("Failed to parse execution request: %v", err)
		return nil
	}

	compilerService := service.NewCompilerService(workerPool, showOutput)

	res, err := compilerService.ExecuteProblemCode(req.Code, req.Language)
	if err != nil {
		log.Printf("Failed to compile code: %v", err)
		return nil
	}

	resData, _ := json.Marshal(res)
	return resData
}

func HandleCompilerRequestBytes(data []byte, workerPool *executor.WorkerPool, showOutput bool) []byte {
	return HandleCompilerRequest(data, workerPool, showOutput)
}

func HandleProblemRunRequestBytes(data []byte, workerPool *executor.WorkerPool, showOutput bool) []byte {
	return HandleProblemRunRequest(data, workerPool, showOutput)
}
