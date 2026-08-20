package operator

/*
Pseudo code for now. + maybe find a better name for the file

This controller watches:
- the directive CRD, the main thing it reacts to, it gates changes to when the member actually has to act
- the MDBMulticluster CRD, to hash it when it changes, and to source the actual changes it has to make -> not the timing, but the content of what it must deploy (the name of the resource, the services configuration, TLS etc...). Ignores the member fields in here, instead they come from directive
-

This controller *can* run on the same cluster that is the leader. But the leader will be a completely separate loop that is doing planning only.

Order per reconcile: FIRST echo + report facts (unconditional, even if fences fail), THEN fences, THEN act or hold.
The echo = copy directive metadata.generation into status.observedGeneration. Means "I have SEEN instruction #N", not "I obeyed".
Without it, the leader can't tell "member holds deliberately" from "member never saw it".

Fences (before acting):
- directive targetSpecHash == hash of my local CR copy, else hold (spec fence)
- directive leadership term >= my local lease term, else hold (term fence)
  NOT strict equality: a newer term = legit leader elected without my cluster (I was the minority, my lease is just stale, next renewal catches it up). The fence rejects the past, not the future.

It writes to:
- STS, secrets, configmaps, services... all resources that are the actual workloads, or their configurations
- maybe certs
- the directive status (only the local one)
- the MDBMulticluster CRD status, according to what the leader instructs it to write, via the directive

- report progress in its directive status:
  - hash it observes
  - directive generation
  - was the sts applied ?
  - is the agent registered ?
  - am i in goal state ?

It needs to talk to OM for some status reporting (agent registered ? goal state ?), but *read only*. Never writes OM: single AC writer = the leader.

Has the kubernetes permissions to act on everything local. Must not write to directive *specs*



*/
