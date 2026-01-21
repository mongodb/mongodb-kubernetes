do
      print("Loading MongoDB gRPC dissector v7 (with JSON)...")

      local mongo_dissector = Dissector.get("mongo")
      local mongo_grpc = Proto("mongo_grpc", "MongoDB Wire Protocol (gRPC)")

      -- Proto fields for JSON output
      local pf_json = ProtoField.string("mongo_grpc.json", "JSON")
      mongo_grpc.fields = { pf_json }

      local grpc_message_data = Field.new("grpc.message_data")
      local pb_message_name = Field.new("protobuf.message.name")

      -- BSON to JSON converter
      local function bson_to_json(tvb, offset, indent)
          indent = indent or 0
          local ind = string.rep("  ", indent)
          local ind2 = string.rep("  ", indent + 1)

          if offset + 4 > tvb:len() then return "{}", offset end

          local doc_len = tvb(offset, 4):le_uint()
          local doc_end = offset + doc_len
          local pos = offset + 4
          local elements = {}

          while pos < doc_end - 1 and pos < tvb:len() do
              local elem_type = tvb(pos, 1):uint()
              pos = pos + 1

              if elem_type == 0 then break end  -- End of document

              -- Read element name (null-terminated)
              local name_start = pos
              while pos < tvb:len() and tvb(pos, 1):uint() ~= 0 do
                  pos = pos + 1
              end
              local name = tvb(name_start, pos - name_start):string()
              pos = pos + 1  -- skip null

              local value

              if elem_type == 0x01 then  -- Double
                  value = tostring(tvb(pos, 8):le_float())
                  pos = pos + 8
              elseif elem_type == 0x02 then  -- String
                  local str_len = tvb(pos, 4):le_uint()
                  pos = pos + 4
                  value = '"' .. tvb(pos, str_len - 1):string():gsub('"', '\\"') .. '"'
                  pos = pos + str_len
              elseif elem_type == 0x03 then  -- Document
                  value, pos = bson_to_json(tvb, pos, indent + 1)
              elseif elem_type == 0x04 then  -- Array
                  local arr_json, new_pos = bson_to_json(tvb, pos, indent + 1)
                  -- Convert object to array format
                  value = arr_json:gsub('"[0-9]+":', '')
                  pos = new_pos
              elseif elem_type == 0x05 then  -- Binary
                  local bin_len = tvb(pos, 4):le_uint()
                  pos = pos + 4
                  local subtype = tvb(pos, 1):uint()
                  pos = pos + 1
                  value = '"<Binary, subtype=' .. subtype .. ', len=' .. bin_len .. '>"'
                  pos = pos + bin_len
              elseif elem_type == 0x07 then  -- ObjectId
                  local oid = ""
                  for i = 0, 11 do
                      oid = oid .. string.format("%02x", tvb(pos + i, 1):uint())
                  end
                  value = '{"$oid": "' .. oid .. '"}'
                  pos = pos + 12
              elseif elem_type == 0x08 then  -- Boolean
                  value = tvb(pos, 1):uint() == 1 and "true" or "false"
                  pos = pos + 1
              elseif elem_type == 0x09 then  -- DateTime
                  local ts = tvb(pos, 8):le_uint64()
                  value = '{"$date": ' .. tostring(ts) .. '}'
                  pos = pos + 8
              elseif elem_type == 0x0A then  -- Null
                  value = "null"
              elseif elem_type == 0x10 then  -- Int32
                  value = tostring(tvb(pos, 4):le_int())
                  pos = pos + 4
              elseif elem_type == 0x11 then  -- Timestamp
                  local ts = tvb(pos, 8):le_uint64()
                  value = '{"$timestamp": ' .. tostring(ts) .. '}'
                  pos = pos + 8
              elseif elem_type == 0x12 then  -- Int64
                  local val = tvb(pos, 8):le_int64()
                  value = tostring(val)
                  pos = pos + 8
              else
                  value = '"<unknown type ' .. elem_type .. '>"'
                  break
              end

              table.insert(elements, ind2 .. '"' .. name .. '": ' .. value)
          end

          local json = "{\n" .. table.concat(elements, ",\n") .. "\n" .. ind .. "}"
          return json, doc_end
      end

      function mongo_grpc.dissector(tvb, pinfo, tree)
          local msg_names = {pb_message_name()}

          local in_wire_message = false
          for _, mname in ipairs(msg_names) do
              if tostring(mname) == "mongodb.WireMessage" then
                  in_wire_message = true
                  break
              end
          end

          if not in_wire_message then return end

          local grpc_data_fields = {grpc_message_data()}

          for i, gdata in ipairs(grpc_data_fields) do
              if gdata and gdata.range then
                  local data_tvb = gdata.range:tvb()
                  local data_len = data_tvb:len()

                  if data_len >= 16 then
                      local mongo_len = data_tvb(0, 4):le_uint()

                      if mongo_len == data_len then
                          pinfo.cols.protocol = "MONGO/gRPC"
                          local subtree = tree:add(mongo_grpc, data_tvb, "MongoDB Wire Protocol")

                          -- Call mongo dissector for tree view
                          mongo_dissector:call(data_tvb, pinfo, subtree)

                          -- Extract and show JSON for OP_MSG
                          local opcode = data_tvb(12, 4):le_uint()
                          if opcode == 2013 and data_len > 21 then
                              local section_kind = data_tvb(20, 1):uint()
                              if section_kind == 0 then
                                  local bson_start = 21
                                  local json_str, _ = bson_to_json(data_tvb, bson_start, 0)

                                  -- Add JSON as a subtree item
                                  local json_tree = subtree:add(pf_json, data_tvb(bson_start), "JSON")
                                  for line in json_str:gmatch("[^\n]+") do
                                    json_tree:add(pf_json, data_tvb(bson_start, 0), line)
                                  end
                              end
                          end
                      end
                  end
              end
          end
      end

      register_postdissector(mongo_grpc)
      print("SUCCESS: Registered v7")
  end
