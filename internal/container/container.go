package container

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/grussorusso/serverledge/internal/executor"
)

// NewContainer creates and starts a new container.
func NewContainer(image, codeTar string, opts *ContainerOptions) (ContainerID, error) {
	contID, err := cf.Create(image, opts)
	if err != nil {
		log.Printf("Failed container creation")
		return "", err
	}

	if len(codeTar) > 0 {
		decodedCode, _ := base64.StdEncoding.DecodeString(codeTar)
		err = cf.CopyToContainer(contID, bytes.NewReader(decodedCode), "/app/")
		if err != nil {
			log.Printf("Failed code copy")
			return "", err
		}
	}

	err = cf.Start(contID)
	if err != nil {
		return "", err
	}

	return contID, nil
}

// Execute interacts with the Executor running in the container to invoke the
// function through a HTTP request.
func Execute(contID ContainerID, req *executor.InvocationRequest) (*executor.InvocationResult, time.Duration, error) {
	ipAddr, err := cf.GetIPAddress(contID)
	if err != nil {
		return nil, 0, fmt.Errorf("Failed to retrieve IP address for container: %v", err)
	}

	postBody, _ := json.Marshal(req)
	postBodyB := bytes.NewBuffer(postBody)
	resp, waitDuration, err := sendPostRequestWithRetries(fmt.Sprintf("http://%s:%d/invoke", ipAddr,
		executor.DEFAULT_EXECUTOR_PORT), postBodyB)
	if err != nil || resp == nil {
		return nil, waitDuration, fmt.Errorf("Request to executor failed: %v", err)
	}
	defer resp.Body.Close()

	d := json.NewDecoder(resp.Body)
	response := &executor.InvocationResult{}
	err = d.Decode(response)
	if err != nil {
		return nil, waitDuration, fmt.Errorf("Parsing executor response failed: %v", err)
	}

	return response, waitDuration, nil
}

func GetMemoryMB(id ContainerID) (int64, error) {
	return cf.GetMemoryMB(id)
}

func Destroy(id ContainerID) error {
	return cf.Destroy(id)
}

func sendPostRequestWithRetries(url string, body *bytes.Buffer) (*http.Response, time.Duration, error) {
	//EDITED original 15
	const maxRetries = 50
	const backoffMillis = 50
	var totalWaitMillis = 0

	var err error

	for retry := 1; retry <= maxRetries; retry++ {
		resp, err := http.Post(url, "application/json", body)
		if err == nil {
			return resp, time.Duration(totalWaitMillis * int(time.Millisecond)), err
		} else if retry > 1 {
			// It is common to have a failure after a cold start, so
			// we avoid logging failures on the first attempt
			log.Printf("Invocation POST failed (attempt %d/%d): %v", retry, maxRetries, err)
		}

		time.Sleep(time.Duration(backoffMillis * int(time.Millisecond)))
		totalWaitMillis += backoffMillis
	}

	return nil, time.Duration(totalWaitMillis * int(time.Millisecond)), err
}
