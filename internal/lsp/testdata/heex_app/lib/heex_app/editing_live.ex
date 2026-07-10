defmodule HeexApp.EditingLive do
  use Phoenix.Component

  alias HeexApp.SharedLib.Worker

  def render(assigns), do: ~H"{Worker.incomplete_call()}"
end
