import {
	IExecuteFunctions,
	ILoadOptionsFunctions,
	IHttpRequestMethods,
	IHttpRequestOptions,
	IDataObject,
	NodeApiError
} from 'n8n-workflow';
import { v5 as uuidv5 } from 'uuid';

export async function omnaraApiRequest(
	this: IExecuteFunctions | ILoadOptionsFunctions,
	method: IHttpRequestMethods,
	endpoint: string,
	body: IDataObject = {},
	qs: IDataObject = {},
	uri?: string,
	option: IDataObject = {},
): Promise<any> {
	const credentials = await this.getCredentials('omnaraApi');

	if (!credentials) {
		throw new NodeApiError(this.getNode(), {
			message: 'No credentials found',
		});
	}

	const options: IHttpRequestOptions = {
		method,
		body,
		qs,
		url: uri || `${credentials.serverUrl}/api/v1${endpoint}`,
		json: true,
		...option,
	};

	try {
		const response = await this.helpers.httpRequestWithAuthentication.call(
			this,
			'omnaraApi',
			options,
		);
		return response;
	} catch (error) {
		throw new NodeApiError(this.getNode(), error as any);
	}
}

export function formatMessageResponse(message: any): IDataObject {
	return {
		id: message.id,
		content: message.content,
		senderType: message.sender_type || message.senderType,
		createdAt: message.created_at || message.createdAt,
		requiresUserInput:
			message.requires_user_input !== undefined
				? message.requires_user_input
				: message.requiresUserInput,
	};
}

export function generateAgentInstanceId(this: IExecuteFunctions, index: number, agentType: string): string {
	// Check if user provided a custom agent instance ID, otherwise generate one
	var key = this.getNodeParameter('agentInstanceId', index) as string;
	if (!key || key.trim() === '') {
		const workflowId = this.getWorkflow().id as string;
		const executionId = this.getExecutionId();
		key = `${String(workflowId)}:${executionId}`;
	}


	key = `${key}:${agentType}`;
    const NAMESPACE = '6ba7b810-9dad-11d1-80b4-00c04fd430c8';
    const agentInstanceId = uuidv5(key, NAMESPACE);
    
    // Validate UUID format
    if (!agentInstanceId || typeof agentInstanceId !== 'string' || agentInstanceId.length !== 36) {
		throw new Error('Failed to generate valid UUID');
	}
    
    return agentInstanceId;
}

export function getAgentType(this: IExecuteFunctions, index: number): string {
	const userProvidedAgentType = this.getNodeParameter('agentType', index) as string;
	if (userProvidedAgentType && userProvidedAgentType.trim() !== '') {
		return userProvidedAgentType;
	}
	
	// Use workflow name as default
	const workflowName = this.getWorkflow().name as string;
	return workflowName || 'n8n agent';
}