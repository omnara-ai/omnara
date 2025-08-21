import {
	IExecuteFunctions,
	INodeExecutionData,
	INodeProperties,
	NodeOperationError,
} from 'n8n-workflow';

import { omnaraApiRequest } from '../../../../utils/GenericFunctions';

export const endSessionDescription: INodeProperties[] = [
	{
		displayName: 'Agent Instance ID',
		name: 'agentInstanceId',
		type: 'string',
		default: '',
		required: true,
		displayOptions: {
			show: {
				resource: ['session'],
				operation: ['end'],
			},
		},
		description: 'The ID of the agent instance session to end',
	},
];

export async function execute(
	this: IExecuteFunctions,
	index: number,
): Promise<INodeExecutionData[]> {
	const agentInstanceId = this.getNodeParameter('agentInstanceId', index) as string;

	if (!agentInstanceId) {
		throw new NodeOperationError(
			this.getNode(),
			'Agent Instance ID is required',
			{ itemIndex: index },
		);
	}

	const body = {
		agent_instance_id: agentInstanceId,
	};

	try {
		const response = await omnaraApiRequest.call(
			this,
			'POST',
			'/sessions/end',
			body,
		);

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