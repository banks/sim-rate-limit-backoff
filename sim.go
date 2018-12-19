package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"time"

	chart "github.com/wcharczuk/go-chart"
	"golang.org/x/time/rate"
)

type config struct {
	RateLimit         float64
	BurstLimitPerCent int
	NClients          int
	ProcessTime       time.Duration
	WaitTime          time.Duration
	MinBackoff        time.Duration
	MaxBackoff        time.Duration
	BackoffMult       float64
	JitterMult        float64
	BackoffPhased     bool
	InitialSpread     time.Duration

	serverCh  chan chan bool
	collectCh chan *point
	stop      chan struct{}
}

type point struct {
	Start, End time.Time
	OK         bool
}

func main() {
	var cfg config
	var inputFile string

	cfg.stop = make(chan struct{})

	flag.Float64Var(&cfg.RateLimit, "rate-limit", 50, "the rate per second to limit")
	flag.IntVar(&cfg.BurstLimitPerCent, "burst-percent", 10, "the percent of rate limit allowed to burst above")
	flag.IntVar(&cfg.NClients, "clients", 1000, "the number of clients")
	flag.DurationVar(&cfg.ProcessTime, "process-time", 20*time.Millisecond, "the time each RPC takes to process if it's not rate limited")
	flag.DurationVar(&cfg.WaitTime, "wait-time", 0, "the time each RPC will wait for a slot before rate limiting 0 to disable waiting")
	flag.DurationVar(&cfg.MinBackoff, "min-backoff", time.Second, "the minimum backoff")
	flag.DurationVar(&cfg.MaxBackoff, "max-backoff", 5*time.Minute, "the maximum base backoff (jitter still added)")
	flag.Float64Var(&cfg.BackoffMult, "backoff-mult", 2, "the base of the exponent used for backoff")
	flag.BoolVar(&cfg.BackoffPhased, "backoff-phased", false, "if true runs backoff in phases of `initial-spread` with one attempt per phase")
	flag.Float64Var(&cfg.JitterMult, "jitter-mult", 2, "the multiplier of the basic backoff over which jitter is applied")
	flag.DurationVar(&cfg.InitialSpread, "initial-spread", 20*time.Second, "the initial spread of request")
	flag.StringVar(&inputFile, "data-file", "", "if specified the simulation is skipped and the graph drawn immediately from the data in the named file")

	flag.Parse()

	if inputFile != "" {
		data := readDataFile(inputFile)
		drawChart(data.Start, data.End, data.Points, data.Cfg)
		return
	}

	// Start a data collector that will consume the collected results and graph
	// them.
	collectCh, ctx, cancel := collector(cfg)
	defer cancel()
	cfg.collectCh = collectCh

	// Start a server listening on a chan that simulates our network
	cfg.serverCh = server(ctx, cfg)

	// Start clients
	for i := 0; i < cfg.NClients; i++ {
		go client(ctx, i, cfg)
	}

	// Wait for signal
	sigCh := make(chan os.Signal)
	signal.Notify(sigCh, os.Kill, os.Interrupt)
	select {
	case <-sigCh:
		cancel()
	case <-ctx.Done():
	}
	<-cfg.stop
}

func collector(cfg config) (chan *point, context.Context, func()) {
	ch := make(chan *point, 1024)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		defer close(cfg.stop)
		defer cancel()
		points := make([]*point, 0, 100000)
		okCount := 0

		var spreadPassedCh <-chan time.Time
		if cfg.InitialSpread > 0 {
			spreadPassedCh = time.After(cfg.InitialSpread)
		}

		start := time.Now()
		ticker := time.NewTicker(1 * time.Second)
		lastOKCount := 0
		lastLen := 0
		for {
			select {
			case <-spreadPassedCh:
				fmt.Printf("% 4ds    initial spread of %s passed.\n", int(time.Since(start).Seconds()), cfg.InitialSpread)
			case <-ticker.C:
				fmt.Printf("% 4ds  % 8d ok % 8d/s effective rate % 8d/s total rate\n",
					int(time.Since(start).Seconds()), okCount, okCount-lastOKCount, len(points)-lastLen)
				lastOKCount = okCount
				lastLen = len(points)
			case point := <-ch:
				points = append(points, point)
				if point.OK {
					okCount++
				}
				if okCount == cfg.NClients {
					end := time.Now()
					fmt.Printf("DONE: %s total time to deliver to all clients\n", end.Sub(start))
					saveAndDrawChart(start, end, points, cfg)
					return
				}
			case <-ctx.Done():
				end := time.Now()
				fmt.Printf("INCOMPLETE: %s total time to deliver to %d clients\n", end.Sub(start), okCount)
				saveAndDrawChart(start, end, points, cfg)
				return
			}
		}
	}()

	return ch, ctx, cancel
}

type runData struct {
	Cfg        config
	Start, End time.Time
	Points     []*point
}

func writeData(start, end time.Time, points []*point, cfg config) {
	fileName := fmt.Sprintf("sim-data-%s.json", time.Now().Format(time.RFC3339))
	f, err := os.Create(fileName)
	if err != nil {
		panic(err)
	}

	enc := json.NewEncoder(f)

	err = enc.Encode(runData{
		Cfg:    cfg,
		Start:  start,
		End:    end,
		Points: points,
	})
	if err != nil {
		panic(err)
	}
}

func readDataFile(name string) runData {
	f, err := os.Open(name)
	if err != nil {
		panic(err)
	}

	dec := json.NewDecoder(f)

	var data runData
	err = dec.Decode(&data)
	if err != nil {
		panic(err)
	}
	return data
}

func saveAndDrawChart(start, end time.Time, points []*point, cfg config) {
	writeData(start, end, points, cfg)
	drawChart(start, end, points, cfg)
}

func drawChart(start, end time.Time, points []*point, cfg config) {
	// Process the raw points into bucket histograms
	hist := make(map[int]struct {
		ok     int
		failed int
	})

	for _, p := range points {
		secs := int(p.End.Sub(start).Seconds())
		bucket := hist[secs]
		if p.OK {
			bucket.ok++
		} else {
			bucket.failed++
		}
		hist[secs] = bucket
	}

	// Process the buckets into points on scatter series with y height indicating
	// number of requests in that second.
	var xOK, yOK, xFail, yFail []float64
	var ticks []chart.Tick
	var maxRate int

	for secs, bucket := range hist {
		for i := 0; i < bucket.ok; i++ {
			xOK = append(xOK, float64(secs))
			yOK = append(yOK, float64(i+1))
		}
		for i := 0; i < bucket.failed; i++ {
			xFail = append(xFail, float64(secs))
			yFail = append(yFail, float64(i+1+bucket.ok))
		}
		ticks = append(ticks, chart.Tick{Value: float64(secs), Label: strconv.Itoa(secs)})
		if (bucket.ok + bucket.failed) > maxRate {
			maxRate = bucket.ok + bucket.failed
		}
	}

	// Annotate where the optimal time would be.
	optimal := float64(cfg.NClients) / cfg.RateLimit

	grid := []chart.GridLine{
		{Value: optimal},
	}

	backoffDesc := fmt.Sprintf("%s initial spread, [%s, %s) base jitter",
		cfg.InitialSpread, cfg.MinBackoff, cfg.MaxBackoff)
	if cfg.BackoffPhased {
		backoffDesc = fmt.Sprintf("phased backoff, %s per phase", cfg.InitialSpread)
	}

	graph := chart.Chart{
		Title: fmt.Sprintf("Rate limit %.0f/s (%d%% burst, %s wait), %d clients, %s",
			cfg.RateLimit, cfg.BurstLimitPerCent, cfg.WaitTime, cfg.NClients, backoffDesc),
		TitleStyle: chart.StyleShow(),
		Background: chart.Style{
			Padding: chart.Box{
				Top:    50,
				Left:   30,
				Bottom: 20,
				Right:  20,
			},
		},
		XAxis: chart.XAxis{
			Style: chart.StyleShow(),
			//Ticks:     ticks,
			Name:           "Seconds Elapsed",
			NameStyle:      chart.StyleShow(),
			ValueFormatter: chart.IntValueFormatter,
			GridMajorStyle: chart.Style{
				Show:        true,
				StrokeColor: chart.ColorRed,
				StrokeWidth: 2.0,
			},
			GridLines: grid,
		},
		YAxis: chart.YAxis{
			Style:          chart.StyleShow(),
			Name:           "Requests Served",
			NameStyle:      chart.StyleShow(),
			AxisType:       chart.YAxisSecondary,
			ValueFormatter: chart.IntValueFormatter,
			Range: &chart.ContinuousRange{
				Min: 0.0,
				Max: float64(maxRate),
			},
		},
		Series: []chart.Series{
			chart.ContinuousSeries{
				Name: "Successful Req",
				Style: chart.Style{
					Show:        true,
					StrokeWidth: chart.Disabled,
					DotWidth:    2,
					DotColor:    chart.ColorGreen,
				},
				XValues: xOK,
				YValues: yOK,
			},
			chart.ContinuousSeries{
				Name: "Rate Limited Req",
				Style: chart.Style{
					Show:        true,
					StrokeWidth: chart.Disabled,
					DotWidth:    2,
					DotColor:    chart.ColorAlternateGray,
				},
				XValues: xFail,
				YValues: yFail,
			},
			chart.AnnotationSeries{
				Annotations: []chart.Value2{
					{
						XValue: optimal,
						YValue: float64(maxRate),
						Label:  fmt.Sprintf("Optimal %.1fs", optimal),
						Style: chart.Style{
							StrokeColor: chart.ColorRed,
						},
					},
				},
			},
		},
	}

	//note we have to do this as a separate step because we need a reference to graph
	// graph.Elements = []chart.Renderable{
	// 	chart.LegendThin(&graph),
	// }

	f, err := os.Create("output.png")
	if err != nil {
		log.Println(err.Error())
		return
	}
	err = graph.Render(chart.PNG, f)
	if err != nil {
		log.Println(err.Error())
	}
	exec.Command("open", "output.png").Run()
}

func server(ctx context.Context, cfg config) chan chan bool {
	ch := make(chan chan bool, 1024)

	burst := 1
	if cfg.BurstLimitPerCent > 0 {
		burst = int(cfg.RateLimit * (float64(cfg.BurstLimitPerCent) / 100.0))
	}
	lim := rate.NewLimiter(rate.Limit(cfg.RateLimit), burst)

	go func() {
		for {
			select {
			case respCh := <-ch:
				// Handle each request in it's own goroutine like a real server, esp.
				// since we might be sleeping...
				go func() {
					// Apply rate limit and respond accordingly. It should be buffered so
					// the chan send shouldn't ever block.
					ok := false
					if cfg.WaitTime > 0 {
						waitCtx, cancel := context.WithTimeout(ctx, cfg.WaitTime)
						if lim.Wait(waitCtx) == nil {
							ok = true
						}
						cancel()
					} else {
						ok = lim.Allow()
					}
					if ok {
						time.Sleep(cfg.ProcessTime)
					}
					respCh <- ok
				}()
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch
}

func client(ctx context.Context, i int, cfg config) {
	// Initial spread
	delay := time.Nanosecond
	if cfg.InitialSpread > 0 {
		delay = jitter(0, cfg.InitialSpread)
	}
	waitCh := time.After(delay)
	failures := 0
	start := time.Now()
	for {
		select {
		case <-waitCh:
			// Send a request
			ch := make(chan bool)
			p := point{}
			p.Start = time.Now()
			cfg.serverCh <- ch
			p.OK = <-ch
			p.End = time.Now()
			// Deliver out data point
			cfg.collectCh <- &p
			if p.OK {
				// We are done
				return
			}
			// Need to wait for backoff
			delay := cfg.MinBackoff
			if cfg.BackoffPhased {
				// Figure out which "window" we are in after "the event"
				windowStart := start.Add(time.Duration(failures) * cfg.InitialSpread)
				nextWindowStart := windowStart.Add(cfg.InitialSpread)
				// Need to wait until the end of the current window + some jitter into
				// the next one.
				delay = nextWindowStart.Sub(time.Now()) + jitter(0, cfg.InitialSpread)
			} else {
				min := time.Duration(float64(cfg.MinBackoff) * math.Pow(cfg.BackoffMult, float64(failures)))
				if min > cfg.MaxBackoff {
					min = cfg.MaxBackoff
				}
				max := time.Duration(cfg.JitterMult) * min
				if max > min {
					delay = jitter(min, max)
				} else if min > max {
					delay = jitter(max+1, min)
				}
			}
			waitCh = time.After(delay)
			failures++
		case <-ctx.Done():
			return
		}
	}
}

func jitter(min time.Duration, max time.Duration) time.Duration {
	return min + time.Duration(rand.Int63n(int64(max-min)))
}
