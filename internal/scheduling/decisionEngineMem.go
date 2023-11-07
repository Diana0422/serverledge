package scheduling

import (
	"github.com/grussorusso/serverledge/internal/config"
	"github.com/grussorusso/serverledge/internal/node"
	"log"
	"math/rand"
	"time"
)

type decisionEngineMem struct {
	m map[string]*FunctionInfo
}

func (d *decisionEngineMem) Decide(r *scheduledRequest) int {
	name := r.Fun.Name
	class := r.ClassService

	prob := rGen.Float64()

	var pL float64
	var pC float64
	var pE float64
	var pD float64

	var cFInfo *ClassFunctionInfo

	arrivalChannel <- arrivalRequest{r, class.Name}

	fInfo, prs := d.m[name]
	if !prs {
		pL = startingLocalProb
		pC = startingCloudOffloadProb
		pE = startingEdgeOffloadProb
		pD = 1 - (pL + pC + pE)
	} else {
		cFInfo, prs = fInfo.invokingClasses[class.Name]
		if !prs {
			pL = startingLocalProb
			pC = startingCloudOffloadProb
			pE = startingEdgeOffloadProb
			pD = 1 - (pL + pC + pE)
		} else {
			pL = cFInfo.probExecuteLocal
			pC = cFInfo.probOffloadCloud
			pE = cFInfo.probOffloadEdge
			pD = cFInfo.probDrop
		}
	}

	log.Println("Probabilities are", pL, pC, pE, pD)

	if policyFlag == "edgeCloud" {
		if !r.CanDoOffloading {
			// Can be executed only locally or dropped
			if pL == 0 && pD == 0 && canExecute(r.Fun) {
				pL = 1
				pD = 0
				pC = 0
				pE = 0
			} else if pL == 0 && pD == 0 && !canExecute(r.Fun) {
				pL = 0
				pD = 1
				pC = 0
				pE = 0
			} else {
				pD = pD / (pD + pL)
				pL = pL / (pD + pL)
				pC = 0
				pE = 0
			}
		} else if !canExecute(r.Fun) {
			// Node can't execute function locally
			if pD == 0 && pC == 0 && pE == 0 {
				pD = 0
				pC = 0.5
				pE = 0.5
				pL = 0
			} else {
				pD = pD / (pD + pC + pE)
				pC = pC / (pD + pC + pE)
				pE = pE / (pD + pC + pE)
				pL = 0
			}
		}
	} else {
		if !r.CanDoOffloading {
			// Can be executed only locally or dropped
			if pL == 0 && pD == 0 && canExecute(r.Fun) {
				pL = 1
				pD = 0
				pC = 0
				pE = 0
			} else if pL == 0 && pD == 0 && !canExecute(r.Fun) {
				pL = 0
				pD = 1
				pC = 0
				pE = 0
			} else {
				pD = pD / (pD + pL)
				pL = pL / (pD + pL)
				pC = 0
				pE = 0
			}
		} else if !canExecute(r.Fun) {
			// Node can't execute function locally
			if pD == 0 && pC == 0 && pE == 0 {
				pD = 0
				pC = 1
				pE = 0
				pL = 0
			} else {
				pD = pD / (pD + pC)
				pC = pC / (pD + pC)
				pE = 0
				pL = 0
			}
		}
	}

	if prob <= pL {
		log.Println("Execute LOCAL")
		return LOCAL_EXEC_REQUEST
	} else if prob <= pL+pC {
		log.Println("Execute CLOUD OFFLOAD")
		return CLOUD_OFFLOAD_REQUEST
	} else if prob <= pL+pC+pE && policyFlag == "edgeCloud" {
		log.Println("Execute EDGE OFFLOAD")
		return EDGE_OFFLOAD_REQUEST
	} else {
		log.Println("Execute DROP")
		requestChannel <- completedRequest{
			scheduledRequest: r,
			dropped:          true,
		}

		return DROP_REQUEST
	}
}

func (d *decisionEngineMem) InitDecisionEngine() {
	s := rand.NewSource(time.Now().UnixNano())
	rGen = rand.New(s)

	evaluationInterval = time.Duration(config.GetInt(config.SOLVER_EVALUATION_INTERVAL, 10)) * time.Second
	log.Println("Evaluation interval:", evaluationInterval)

	d.m = make(map[string]*FunctionInfo)

	go d.ShowData()
	go d.handler()
}

/*
Function that:
- Handles the evaluation and calculation of the cold start probabilities.
- Writes the report of the request completion into the data store (influxdb).
- With the arrival of a new request, initializes new FunctionInfo and ClassFunctionInfo objects.
*/
func (d *decisionEngineMem) handler() {
	evaluationTicker :=
		time.NewTicker(evaluationInterval)
	pcoldTicker :=
		time.NewTicker(time.Duration(config.GetInt(config.CONTAINER_EXPIRATION_TIME, 600)) * time.Second)

	for {
		select {
		case _ = <-evaluationTicker.C: // Evaluation handler
			s := rand.NewSource(time.Now().UnixNano())
			rGen = rand.New(s)
			log.Println("Evaluating")

			//Check if there are some instances with 0 arrivals
			for fName, fInfo := range d.m {
				for cName, cFInfo := range fInfo.invokingClasses {
					//Cleanup
					if cFInfo.arrivalCount == 0 {
						cFInfo.timeSlotsWithoutArrivals++
						if cFInfo.timeSlotsWithoutArrivals >= maxTimeSlots {
							d.Delete(fName, cName)
						}
					}
				}
			}

			d.updateProbabilities()

			//Reset arrivals for the time slot
			for _, fInfo := range d.m {
				for _, cFInfo := range fInfo.invokingClasses {
					//Cleanup
					cFInfo.arrivalCount = 0
					cFInfo.arrivals = 0
				}
			}

		case r := <-requestChannel: // Result storage handler
			// New request completed - data is updated in local memory - need to differentiate between edge offloading and cloud offloading
			// Also need to increment the number of blocked requests in the node if this is the case

			// If the request was dropped, then update the respective value in the node structure
			if r.dropped {
				node.Resources.DropRequestsCount += 1
			}

			d.updateData(r)
		case arr := <-arrivalChannel: // Arrival handler - structures initialization
			// A new request is arrived: update the counter of incoming request in the node structure
			node.Resources.RequestsCount += 1

			name := arr.Fun.Name

			fInfo, prs := d.m[name]
			if !prs {
				fInfo = &FunctionInfo{
					name:            name,
					memory:          arr.Fun.MemoryMB,
					cpu:             arr.Fun.CPUDemand,
					probCold:        [3]float64{1, 1, 1},
					bandwidthCloud:  0,
					bandwidthEdge:   0,
					invokingClasses: make(map[string]*ClassFunctionInfo)}

				d.m[name] = fInfo
			}

			cFInfo, prs := fInfo.invokingClasses[arr.class]
			if !prs {
				cFInfo = &ClassFunctionInfo{FunctionInfo: fInfo,
					probExecuteLocal:         startingLocalProb,
					probOffloadCloud:         startingCloudOffloadProb,
					probOffloadEdge:          startingEdgeOffloadProb,
					probDrop:                 1 - (startingLocalProb + startingCloudOffloadProb),
					arrivals:                 0,
					arrivalCount:             0,
					timeSlotsWithoutArrivals: 0,
					className:                arr.class}

				fInfo.invokingClasses[arr.class] = cFInfo
			}

			cFInfo.arrivalCount++
			cFInfo.arrivals = cFInfo.arrivalCount / float64(evaluationInterval)
			cFInfo.timeSlotsWithoutArrivals = 0

		case _ = <-pcoldTicker.C:
			//Reset arrivals for the time slot
			for _, fInfo := range d.m {
				fInfo.coldStartCount = [3]int64{0, 0, 0}
				fInfo.timeSlotCount = [3]int64{0, 0, 0}
			}
		}
	}
}

func (d *decisionEngineMem) updateProbabilities() {
	solve(d.m)
}

func (d *decisionEngineMem) ShowData() {
	for {
		time.Sleep(time.Second * 10)
		for _, fInfo := range d.m {
			for _, cFInfo := range fInfo.invokingClasses {
				log.Println(cFInfo)
			}
		}
	}
	//log.Println("ERLANG: ", ErlangB(57, 45))
	//for {
	//	time.Sleep(5 * time.Second)
	//	log.Println("map", FunctionMap)
	//}
	/*
		for {
			time.Sleep(5 * time.Second)
			for _, functionMap := range FunctionMap {
				for _, finfo := range functionMap {
					log.Println(finfo)
				}
			}
		}
	*/
}

// FIXME: this is now into metricGrabber!!! call the method inside!
func (d *decisionEngineMem) Completed(r *scheduledRequest, offloaded int) {
	if offloaded == 0 {
		log.Printf("LOCAL RESULT %s - Duration: %f, InitTime: %f", r.Fun.Name, r.ExecReport.Duration, r.ExecReport.InitTime)
	} else if offloaded == 1 {
		log.Printf("VERTICAL OFFLOADING RESULT %s - Duration: %f, InitTime: %f", r.Fun.Name, r.ExecReport.Duration, r.ExecReport.InitTime)
	} else {
		log.Printf("HORIZONTAL OFFLOADING RESULT %s - Duration: %f, InitTime: %f", r.Fun.Name, r.ExecReport.Duration, r.ExecReport.InitTime)
	}

	requestChannel <- completedRequest{
		scheduledRequest: r,
		location:         offloaded,
		dropped:          false,
	}

}

func (d *decisionEngineMem) Delete(function string, class string) {
	fInfo, prs := d.m[function]
	if !prs {
		return
	}

	delete(fInfo.invokingClasses, class)

	//If there aren't any more classes calls the function can be deleted
	if len(fInfo.invokingClasses) == 0 {
		delete(d.m, function)
	}
}

/*
// TODO maybe remove
func UpdateDataAsync(r function.Response) {
	name := r.Name
	class := r.Class

	var location int

	if r.CloudOffloadLatency != 0 {
		location = LOCAL
	} else {
		location = OFFLOADED_CLOUD
	}

	//TODO edit this
	fInfo, prs := de.FunctionMap[name]
	if !prs {
		// If it is missing from the map then enough time has passed to cause expiring on the function entry,
		// or the invocation came from somewhere else.
		// This means that maybe is not necessary to maintain information about this function
		return
	}

	fInfo.count[location] = fInfo.count[location] + 1
	fInfo.timeSlotCount[location] = fInfo.timeSlotCount[location] + 1

	//Welford mean and variance
	diff := r.Duration - fInfo.meanDuration[location]
	fInfo.meanDuration[location] = fInfo.meanDuration[location] +
		(1/float64(fInfo.count[location]))*(diff)
	diff2 := r.Duration - fInfo.meanDuration[location]

	fInfo.varianceDuration[location] = (diff * diff2) / float64(fInfo.count[location])

	if !r.IsWarmStart {
		diff := r.InitTime - fInfo.initTime[location]
		fInfo.initTime[location] = fInfo.initTime[location] +
			(1/float64(fInfo.count[location]))*(diff)

		fInfo.coldStartCount[location]++
	}

	if r.CloudOffloadLatency != 0 {
		diff := r.CloudOffloadLatency - CloudOffloadLatency
		CloudOffloadLatency = CloudOffloadLatency +
			(1/float64(fInfo.count[location]))*(diff)
	}

	//TODO maybe remove
	if r.ResponseTime > Classes[class].MaximumResponseTime {
		fInfo.missed++
	}
}
*/

func (d *decisionEngineMem) updateData(r completedRequest) {
	name := r.Fun.Name

	location := r.location

	fInfo, prs := d.m[name]
	//TODO create here the entry in the function? Is it necessary?
	if !prs {
		return
	}

	fInfo.count[location] = fInfo.count[location] + 1
	fInfo.timeSlotCount[location] = fInfo.timeSlotCount[location] + 1

	//Welford mean and variance
	diff := r.ExecReport.Duration - fInfo.meanDuration[location]
	fInfo.meanDuration[location] = fInfo.meanDuration[location] +
		(1/float64(fInfo.count[location]))*(diff)
	diff2 := r.ExecReport.Duration - fInfo.meanDuration[location]

	fInfo.varianceDuration[location] = (diff * diff2) / float64(fInfo.count[location])

	if !r.ExecReport.IsWarmStart {
		diff := r.ExecReport.InitTime - fInfo.initTime[location]
		fInfo.initTime[location] = fInfo.initTime[location] +
			(1/float64(fInfo.count[location]))*(diff)

		fInfo.coldStartCount[location]++
		fInfo.probCold[location] = float64(fInfo.coldStartCount[location]) / float64(fInfo.timeSlotCount[location])
	}

	// Update offload latency cloud
	if r.ExecReport.OffloadLatencyCloud != 0 {
		diff := r.ExecReport.OffloadLatencyCloud - CloudOffloadLatency
		CloudOffloadLatency = CloudOffloadLatency + (1/float64(fInfo.count[location]))*(diff)
	}

	// Update offload latency edge
	if r.ExecReport.OffloadLatencyEdge != 0 {
		diff := r.ExecReport.OffloadLatencyEdge - EdgeOffloadLatency
		EdgeOffloadLatency = EdgeOffloadLatency + (1/float64(fInfo.count[location]))*(diff)
	}
}
