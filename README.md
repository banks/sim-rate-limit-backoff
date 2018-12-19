# Rate Limit and Backoff Sim

This was written to explore the parameters of a specific problem in Consul. It
could apply in many other cases however it's often likely that rate limits are
to prevent abuse rather than to control expected traffic patterns as is the case
here.

There are many possible improvements that could be made -- this was a quick tool
for a specific task. Displaying a legend in the charts was problematic without 
major investment so the green dots (at the bottom) are "success" requests after 
which that client terminates, while grey ones are "rate limited" requests. The 
request dots are "stacked" one y unit high making the hight of the stack the number 
of requests serveed for that 1-second bucket.

The main learning were:

 1. Exponential backoff is not necessarily the best choice when optimising for
    both total time for each client to get a successful request. In this case
    having each client only retry once per "window" with uniform random
    selection within that window was much better for _both_ overall server 
    load and completion time.

    ![10000 clients with exponential backoff](10000-50-20-burst_10-exp.png)
    ![10000 clients with windowed backoff](10000-50-20-burst_10-phased.png)

 2. The burst setting on a leaky bucket rate limiter (e.g. golang.org/x/time/rate)
    is much more important than initially expected - Since I wanted a "hard"
    rate limit I set the burst to 1 initially but even with many many clients and random
    jitter the arrivals just aren't uniform enough and so fall foul of the rate
    limit far too often. The charts below show identical simulations other than with burst of 1 vs 10% (i.e. burst of 5).

    ![1000 clients with no bursting](1000-50-20-burst-0-exp.png)
    ![1000 clients with bursting](1000-50-20-burst-10-exp.png)

 3. Alternatively to learning 2, rather that adding bursting to the bucket, if
    you can tolerate a small amount of added latency in the request you can
    instead wait up to a short period to see if a token becomes available before
    failing. Even without burst, this has the same effect of smoothing out the
    random arrivals and making the rate limit work. The tradeoff is that during
    your short wait, the server is still expending _some_ resource holding that
    request in memory and connection state etc. In our case I expect a short
    wait (up to say 500ms) to be a good trade since it will lower the overall
    work done - no need to respond with an error and then redo all the parsing
    work etc. on a subsequent retry. The first chart below shows a configuration with no waiting for a token nor bursting. The following graphs all looks essentially the same so any amount of wait and or burst basically gets the same improvement. Note that I had to change the server in the simulation slightly here to run each request in a new goroutine since it actually might block for a while now. I also added a Sleep to simulate actual work done in the happy path in case that skewed results.


    ![5000 clients with no bursting and no wait](5000-50-20-burst-0-wait-0.png)
    ![5000 clients with bursting and no wait](5000-50-20-burst-10-wait-0.png)
    ![5000 clients with no bursting and 100ms wait](5000-50-20-burst-0-wait-100ms.png)
    ![5000 clients with bursting and 100ms wait](5000-50-20-burst-10-wait-100ms.png)
