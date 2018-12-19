#! /bin/bash

go run sim.go -clients 5000 -rate-limit 50 -initial-spread 20s -burst-percent 0 -backoff-phased -wait-time 0s
sleep 1
mv output.png 5000-50-20-burst-0-wait-0.png

go run sim.go -clients 5000 -rate-limit 50 -initial-spread 20s -burst-percent 10 -backoff-phased -wait-time 0s
sleep 1
mv output.png 5000-50-20-burst-10-wait-0.png

go run sim.go -clients 5000 -rate-limit 50 -initial-spread 20s -burst-percent 0 -backoff-phased -wait-time 100ms
sleep 1
mv output.png 5000-50-20-burst-0-wait-100ms.png

go run sim.go -clients 5000 -rate-limit 50 -initial-spread 20s -burst-percent 10 -backoff-phased -wait-time 100ms
sleep 1
mv output.png 5000-50-20-burst-10-wait-100ms.png
