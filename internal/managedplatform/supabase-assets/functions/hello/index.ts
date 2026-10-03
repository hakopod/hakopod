console.log("Hello from Functions!")

export default {
  fetch: async () => Response.json({ message: "Hello from Edge Functions!" }),
}
