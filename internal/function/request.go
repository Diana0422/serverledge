package function

import (
	"fmt"
	"github.com/grussorusso/serverledge/internal/config"
	"time"
)

//Request represents a single function invocation.
type Request struct {
	Fun     *Function
	Params  map[string]string
	Arrival time.Time
	Report  *ExecutionReport
	RequestQoS
}

type RequestQoS struct {
	Class    Priority
	MaxRespT float64
}

type ExecutionReport struct {
	Arrival        time.Time // this is useful for latency computing
	Result         string
	ResponseTime   float64
	IsWarmStart    bool
	InitTime       float64
	OffloadLatency float64
	Duration       float64
	CPUTime        float64
}

func (r *Request) String() string {
	return fmt.Sprintf("Req-%s", r.Fun.Name)
}

var MaxRespTime = config.GetFloat("max.response.time", 200)

type Priority int64

const (
	LOW               Priority = 0
	HIGH_PERFORMANCE           = 1
	HIGH_AVAILABILITY          = 2
)

type InvocationRequest struct {
	Params      map[string]string
	QoSClass    Priority
	QoSMaxRespT float64
}

type IncomingRequest struct {
	Params      map[string]string
	QoSClass    string
	QoSMaxRespT float64
}
