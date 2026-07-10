defmodule HeexApp.PageLive do
  use Phoenix.Component

  alias HeexApp.Components
  alias HeexApp.SharedLib.Worker

  @title "module attribute, not a HEEX assign"

  def render(assigns) do
    ~H"""
    <main>
      <.local_card />
      <.local_card />
      <Components.remote_card />

      <div {dynamic_attrs()}>
        {choose(%{}, Worker.nested_brace_call())}
      </div>

      <p>{
        Worker.multiline_call()
      }</p>

      <div data-label="phx-no-curly-interpolation">
        {Worker.attribute_value_call()}
      </div>

      <script data-visible={assigns.visible?}>
        if (window.count < 2) { Ignored.Script.script_literal_call() }
        <%= Worker.eex_script_call() %>
      </script>

      <style>
        .example { Ignored.Style.style_literal_call() }
        <%= Worker.eex_style_call() %>
      </style>

      <section phx-no-curly-interpolation>
        <div>{Ignored.NoCurly.no_curly_literal_call()}</div>
        <%= Worker.eex_no_curly_call() %>
      </section>

      <p>🔥 {Worker.unicode_call()}</p>

      {@title}
    </main>
    """
  end

  defp local_card(assigns), do: ~H"<span>local</span>"
  defp choose(_map, value), do: value
  defp dynamic_attrs, do: %{class: "dynamic"}
end
