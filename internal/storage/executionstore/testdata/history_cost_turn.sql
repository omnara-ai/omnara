CREATE FUNCTION history_cost_turn(target uuid, k integer, keep_open boolean) RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    agent agents%ROWTYPE;
    revision uuid;
    turn_id uuid := uuidv7();
    input_id uuid := uuidv7();
    input_event_id uuid := uuidv7();
    context_id uuid := uuidv7();
    output_id uuid := uuidv7();
    call_id uuid := uuidv7();
    result_id uuid := uuidv7();
    compact_id uuid := uuidv7();
    checkpoint_id uuid := uuidv7();
    interaction_id uuid := uuidv7();
    config_input_id uuid := uuidv7();
    config_event_id uuid := uuidv7();
    b bigint := 2 + k::bigint * 8;
BEGIN
    SET CONSTRAINTS ALL DEFERRED;
    SELECT * INTO STRICT agent FROM agents WHERE id = target;
    SELECT model.current_revision_id INTO STRICT revision
    FROM agent_configs config JOIN configured_models model ON model.org_id = config.org_id
      AND model.id = config.configured_model_id
    WHERE config.project_id = agent.project_id AND config.id = agent.current_config_id;

    INSERT INTO agent_inputs(id,project_id,agent_id,state,input_kind,delivery_mode,queued_at)
    VALUES (input_id,agent.project_id,target,'received','content','queued',now());
    INSERT INTO content_blocks(agent_id,owner_kind,owner_agent_input_id,ordinal,block_kind,text_content,created_at)
    VALUES (target,'agent_input',input_id,0,'text','input',
      agent_payload_created_at(agent.project_id,target,'agent_input',input_id,false));
    INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,agent_input_id,is_opening_event,idempotency_key,created_at)
    VALUES (input_event_id,target,turn_id,b,'agent_input',input_id,true,'agent_input:'||input_id,
      agent_payload_created_at(agent.project_id,target,'agent_input',input_id,true));
    UPDATE agent_inputs SET state='resolved',admitted_event_id=input_event_id,admitted_at=now(),resolved_at=now()
    WHERE agent_id=target AND id=input_id;
    INSERT INTO agent_turns(id,agent_id,turn_sequence,latest_event_id,latest_semantic_event_id)
    VALUES (turn_id,target,b,input_event_id,input_event_id);

    INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,turn_id,operation_kind,attempt_number,
      agent_config_id,configured_model_revision_id,input_event_sequence,runtime_lock_id,created_at,
      opening_input_ids,opening_event_sequence)
    VALUES (context_id,agent.org_id,agent.project_id,target,turn_id,'normal',1,agent.current_config_id,
      revision,b,context_id,now(),ARRAY[input_id],b);
    INSERT INTO model_outputs(id,agent_id,model_call_context_id,stop_reason,created_at)
    VALUES (output_id,target,context_id,'tool_use',now());
    INSERT INTO tool_calls(id,agent_id,model_output_id,provider_call_id,name,input,type,state,created_at)
    VALUES (call_id,target,output_id,'call','list_agents','{}','built_in','awaiting_authorization',now());
    INSERT INTO content_blocks(agent_id,owner_kind,owner_model_output_id,ordinal,block_kind,tool_call_id,created_at)
    VALUES (target,'model_output',output_id,0,'tool_call',call_id,
      agent_payload_created_at(agent.project_id,target,'model_output',output_id,false));
    INSERT INTO agent_events(agent_id,turn_id,sequence,event_kind,model_output_id,created_at)
    VALUES (target,turn_id,b+1,'model_output',output_id,
      agent_payload_created_at(agent.project_id,target,'model_output',output_id,false));
    UPDATE model_call_contexts SET state='succeeded',api_format='openai-responses',api_variant='default',
      input_tokens_total=1,uncached_input_tokens=1,output_tokens_total=1,input_fingerprint=repeat('a',64),completed_at=now()
    WHERE agent_id=target AND id=context_id;
    INSERT INTO agent_interactions(id,agent_id,tool_call_id,interaction_kind,state,created_at)
    VALUES (interaction_id,target,call_id,'permission','open',now()+k*interval '1 microsecond');
    IF NOT keep_open THEN
      UPDATE agent_interactions SET state='canceled',resolved_at=now()
      WHERE agent_id=target AND id=interaction_id;
    END IF;
    UPDATE tool_calls SET state='completed' WHERE agent_id=target AND id=call_id;
    INSERT INTO tool_call_results(id,agent_id,tool_call_id,outcome,completed_at)
    VALUES (result_id,target,call_id,'succeeded',now());
    INSERT INTO content_blocks(agent_id,owner_kind,owner_tool_call_result_id,ordinal,block_kind,text_content,created_at)
    VALUES (target,'tool_call_result',result_id,0,'text','result',
      agent_payload_created_at(agent.project_id,target,'tool_call_result',result_id,false));
    INSERT INTO agent_events(agent_id,turn_id,sequence,event_kind,tool_call_result_id,created_at)
    VALUES (target,turn_id,b+2,'tool_result',result_id,
      agent_payload_created_at(agent.project_id,target,'tool_call_result',result_id,false));

    INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,turn_id,operation_kind,attempt_number,
      agent_config_id,configured_model_revision_id,input_event_sequence,source_event_sequence_end,runtime_lock_id,
      created_at,opening_input_ids,opening_event_sequence)
    VALUES (compact_id,agent.org_id,agent.project_id,target,turn_id,'compaction',1,agent.current_config_id,
      revision,b+2,b+2,compact_id,now(),ARRAY[input_id],b);
    INSERT INTO context_checkpoints(id,agent_id,summarized_through_event_sequence,producer_model_call_context_id,
      summary,opening_input_ids,opening_event_sequence,created_at)
    VALUES (checkpoint_id,target,b+2,compact_id,'summary',ARRAY[input_id],b,now());
    INSERT INTO agent_events(agent_id,turn_id,sequence,event_kind,context_checkpoint_id,created_at)
    VALUES (target,turn_id,b+3,'context_checkpoint',checkpoint_id,
      agent_payload_created_at(agent.project_id,target,'context_checkpoint',checkpoint_id,false));
    UPDATE model_call_contexts SET state='succeeded',api_format='openai-responses',api_variant='default',
      input_tokens_total=1,uncached_input_tokens=1,output_tokens_total=1,completed_at=now()
    WHERE agent_id=target AND id=compact_id;
    INSERT INTO agent_inputs(id,project_id,agent_id,state,input_kind,delivery_mode,agent_config_id,queued_at)
    VALUES (config_input_id,agent.project_id,target,'received','config_change','immediate',agent.current_config_id,now());
    INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,agent_input_id,idempotency_key,created_at)
    VALUES (config_event_id,target,turn_id,b+4,'agent_input',config_input_id,'agent_input:'||config_input_id,now());
    UPDATE agent_inputs SET state='resolved',admitted_event_id=config_event_id,admitted_at=now(),resolved_at=now(),
      opening_input_ids=ARRAY[input_id],opening_event_sequence=b
    WHERE agent_id=target AND id=config_input_id;
    SET CONSTRAINTS ALL IMMEDIATE;
END;
$$;
