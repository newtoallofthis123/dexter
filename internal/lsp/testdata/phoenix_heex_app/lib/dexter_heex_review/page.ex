defmodule DexterHeexReview.Page do
  use Phoenix.Component

  alias DexterHeexReview.Components
  alias DexterHeexReview.Worker

  @title "module attribute, distinct from the HEEX assign"

  def render(assigns) do
    ~H"""
    <main>
      <.local_card />
      <Components.remote_card title={@title} />

      <div {dynamic_attrs()}>
        {choose(%{}, Worker.run())}
      </div>
      <p>🔥 {Worker.unicode_call()}</p>

      <%= if @visible? do %>
        <.local_card />
      <% end %>

      <%= for item <- @items do %>
        {item.name}
      <% end %>

      <%= for marker <- ["#"] do %>
        <span>{marker}</span>
      <% end %>

      <li :for={entry <- @entries}>{entry.name}</li>

      <script>
        if (window.count < 2) { Ignored.Module.call() }
        window.workerVisible = <%= Worker.visible?() %>
      </script>

      <section phx-no-curly-interpolation>
        {Ignored.Module.call()} {Worker.visible?()}
      </section>
    </main>
    """
  end

  defp local_card(assigns), do: ~H"<span>local</span>"
  defp choose(_map, value), do: value
  defp dynamic_attrs, do: %{class: "dynamic"}
end
