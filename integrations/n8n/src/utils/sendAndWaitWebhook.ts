import {
	IWebhookFunctions,
	IWebhookResponseData,
	INodeExecutionData,
	IDataObject,
} from 'n8n-workflow';

/**
 * Handle webhook responses for Omnara sendAndWait operations
 * This receives the user's response from Omnara and continues the workflow
 */
export async function omnaraSendAndWaitWebhook(
	this: IWebhookFunctions,
): Promise<IWebhookResponseData> {
	const req = this.getRequestObject();
	const resp = this.getResponseObject();
	const headers = this.getHeaderData() as IDataObject;
	const body = this.getBodyData() as IDataObject;
	
	// Check if this is a valid Omnara webhook call
	if (!headers['x-omnara-webhook'] && !body.agent_instance_id) {
		resp.status(400).json({
			error: 'Invalid webhook call - missing Omnara headers',
		});
		return {
			noWebhookResponse: true,
		};
	}

	// Extract the response data from Omnara
	const responseData: IDataObject = {
		userResponse: body.user_message || body.content || '',
		userId: body.user_id,
		messageId: body.message_id,
		agentInstanceId: body.agent_instance_id,
		timestamp: body.timestamp || new Date().toISOString(),
		metadata: body.metadata || {},
	};

	// Send success response back to Omnara
	resp.status(200).json({
		success: true,
		message: 'Webhook received successfully',
		execution_resumed: true,
	});

	// Return the data to continue the workflow
	const returnData: INodeExecutionData[] = [
		{
			json: responseData,
		},
	];

	return {
		workflowData: [returnData],
	};
}

/**
 * Generate the webhook configuration for sendAndWait
 */
export function getOmnaraWebhookConfig(instanceId: string): IDataObject {
	return {
		name: 'default',
		httpMethod: 'POST',
		responseMode: 'onReceived',
		path: `omnara-webhook/${instanceId}`,
		restartWebhook: true,
	};
}