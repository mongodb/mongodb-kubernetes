function envoy_on_request(handle)
  local request = handle:headers()
  local path = request:get(":path") or ""
  if string.sub(path, 1, 8) == "/health/" then
    handle:respond({[":status"] = "200", ["content-type"] = "text/plain"}, "ok")
    return
  end
  local response = {
    [":status"] = "200",
    ["content-type"] = request:get("content-type") or "application/octet-stream",
    ["x-stub-pod"] = os.getenv("HOSTNAME") or "unknown",
    ["x-echo-method"] = request:get(":method") or "",
    ["x-echo-path"] = path,
    ["x-echo-authority"] = request:get(":authority") or "",
  }
  for name, value in pairs(request) do
    if string.sub(name, 1, 1) ~= ":" then
      response["x-echo-" .. name] = value
    end
  end
  local body = handle:body()
  local payload = ""
  if body ~= nil and body:length() > 0 then
    payload = body:getBytes(0, body:length())
  end
  handle:respond(response, payload)
end
