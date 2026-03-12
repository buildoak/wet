Research task: Search the web and find everything about Anthropic's Messages API SSE (Server-Sent Events) streaming format, specifically about token usage reporting.

Answer these specific questions:

1. What SSE events does the Anthropic Messages API emit during a streaming response? List all event types in order.

2. Which event contains usage.input_tokens? Is it message_start, message_delta, or message_stop? What is the exact JSON structure?

3. Which event contains usage.output_tokens? Is it reported incrementally or as a final count?

4. What does a complete SSE stream look like? Give an example showing event types and their data payloads with usage fields.

5. Are there any known issues with input_tokens being 0 in streaming responses? Search for GitHub issues on anthropic-sdk-python, anthropic-sdk-typescript, and community discussions.

6. Does prompt caching affect how input_tokens are reported in the message_start event?

7. How do proxy implementations (litellm, portkey, helicone) extract token usage from Anthropic SSE streams?

8. What is the count_tokens API endpoint? Is it free? Rate limited?

Return a structured report with all findings, including example JSON payloads for each SSE event type.
