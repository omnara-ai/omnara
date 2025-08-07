import type {
	IExecuteFunctions,
	IHttpRequestMethods,
	IRequestOptions,
	IDataObject,
} from 'n8n-workflow';

import { NodeApiError } from 'n8n-workflow';
import { v4 as uuid } from 'uuid';

export async function omnaraApiRequest(
	this: IExecuteFunctions,
	method: IHttpRequestMethods,
	endpoint: string,
	body: any = {},
	query: IDataObject = {},
): Promise<any> {
	const credentials = await this.getCredentials('omnaraApi');
	const baseUrl = (credentials.baseUrl as string).replace(/\/$/, '');

	const options: IRequestOptions = {
		method,
		uri: `${baseUrl}${endpoint}`,
		headers: {
			'Authorization': `Bearer ${credentials.apiKey}`,
			'Content-Type': 'application/json',
		},
		body,
		qs: query,
		json: true,
	};

	try {
		return await this.helpers.request(options);
	} catch (error) {
		throw new NodeApiError(this.getNode(), error);
	}
}

export async function sendAndWaitForApproval(
	this: IExecuteFunctions,
	message: string,
	agentType: string,
	agentInstanceId: string,
	additionalOptions: any = {},
): Promise<any> {
	// Generate instance ID if not provided
	const instanceId = agentInstanceId || uuid();

	// Send the message with requires_user_input=true
	const body = {
		content: message,
		agent_type: agentType,
		agent_instance_id: instanceId,
		requires_user_input: true,
		send_push: additionalOptions.sendPush !== false,
		send_email: additionalOptions.sendEmail || false,
		send_sms: additionalOptions.sendSms || false,
	};

	const initialResponse = await omnaraApiRequest.call(
		this,
		'POST',
		'/api/v1/messages/agent',
		body,
	);

	// If we already have queued messages, return them
	if (initialResponse.queued_user_messages && initialResponse.queued_user_messages.length > 0) {
		return {
			...initialResponse,
			response: initialResponse.queued_user_messages[0],
		};
	}

	// Otherwise, poll for response
	const timeoutMinutes = additionalOptions.timeout || 60;
	const pollIntervalSeconds = additionalOptions.pollInterval || 10;
	const timeoutMs = timeoutMinutes * 60 * 1000;
	const pollIntervalMs = pollIntervalSeconds * 1000;
	const startTime = Date.now();

	let lastReadMessageId = initialResponse.message_id;

	while (Date.now() - startTime < timeoutMs) {
		// Wait for poll interval
		await new Promise(resolve => setTimeout(resolve, pollIntervalMs));

		// Check for pending messages
		const query = {
			agent_instance_id: instanceId,
			last_read_message_id: lastReadMessageId,
		};

		try {
			const pendingResponse = await omnaraApiRequest.call(
				this,
				'GET',
				'/api/v1/messages/pending',
				{},
				query,
			);

			// Check if we got messages
			if (pendingResponse.messages && pendingResponse.messages.length > 0) {
				// Return the first user message content
				const userMessage = pendingResponse.messages[0];
				return {
					success: true,
					agent_instance_id: instanceId,
					message_id: initialResponse.message_id,
					response: userMessage.content,
					queued_user_messages: [userMessage.content],
					response_time: new Date().toISOString(),
				};
			}

			// If status is "stale", another process read the messages
			if (pendingResponse.status === 'stale') {
				throw new Error('Another process has already read the response');
			}

		} catch (error) {
			// If it's a timeout, break the loop
			if (Date.now() - startTime >= timeoutMs) {
				break;
			}
			// Otherwise, continue polling (might be a temporary network issue)
			console.error('Polling error:', error);
		}
	}

	// Timeout reached
	throw new Error(`No response received within ${timeoutMinutes} minutes`);
}

export async function sendMessage(
	this: IExecuteFunctions,
	message: string,
	agentType: string,
	agentInstanceId: string,
): Promise<any> {
	// Generate instance ID if not provided
	const instanceId = agentInstanceId || uuid();

	const body = {
		content: message,
		agent_type: agentType,
		agent_instance_id: instanceId,
		requires_user_input: false,
	};

	return await omnaraApiRequest.call(
		this,
		'POST',
		'/api/v1/messages/agent',
		body,
	);
}

export async function endSession(
	this: IExecuteFunctions,
	agentInstanceId: string,
): Promise<any> {
	const body = {
		agent_instance_id: agentInstanceId,
	};

	return await omnaraApiRequest.call(
		this,
		'POST',
		'/api/v1/sessions/end',
		body,
	);
}