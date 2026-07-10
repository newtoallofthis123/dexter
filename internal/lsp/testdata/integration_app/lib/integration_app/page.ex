defmodule IntegrationApp.Page do
  alias IntegrationApp.Math

  def render(value) do
    normalized = normalize(value)
    Math.double(normalized)
  end

  defp normalize(value), do: value
end
