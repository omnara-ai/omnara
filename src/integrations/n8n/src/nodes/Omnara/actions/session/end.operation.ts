import {
	IExecuteFunctions,
	INodeExecutionData,
	INodeProperties,
	NodeOperationError,
} from 'n8n-workflow';

import { omnaraApiRequest, generateAgentInstanceId, getAgentType } from '../../../../utils/GenericFunctions';

export const endSessionDescription: INodeProperties[] = [
	{
		displayName: 'Agent Instance ID',
		name: 'agentInstanceId',
		type: 'string',
		default: '',
		required: false,
		displayOptions: {
			show: {
				resource: ['session'],
				operation: ['end'],
			},
		},
		placeholder: 'e.g. 550e8400-e29b-41d4-a716-446655440000',
		description: 'The ID of the agent instance session to end. If left empty, a default value will be generated.',
	},
];

export async function execute(
	this: IExecuteFunctions,
	index: number,
): Promise<INodeExecutionData[]> {
	const agentType = getAgentType.call(this, 0);
	const agentInstanceId = generateAgentInstanceId.call(this, 0, agentType);
	const body = {
		agent_instance_id: agentInstanceId,
	};

	try {
		const response = await omnaraApiRequest.call(this, 'POST', '/sessions/end', body);

		const result = {
			success: response.success,
			agentInstanceId: response.agent_instance_id,
			finalStatus: response.final_status,
			endedAt: new Date().toISOString(),
		};

		return [{ json: result }];
	} catch (error) {
		throw new NodeOperationError(
			this.getNode(),
			`Failed to end session: ${error instanceof Error ? error.message : String(error)}`,
			{ itemIndex: index },
		);
	}
}
