package modelretry

// OutputNoProgressErrorCode marks an attempt that spent its whole output
// allowance without producing text or a tool call, typically because the model
// kept reasoning until it hit max_tokens.
const OutputNoProgressErrorCode = "model_output_no_progress"
